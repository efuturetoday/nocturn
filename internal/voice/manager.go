package voice

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/speaker"
)

// Manager owns the live voice sessions of a workspace, at most one per device.
//
// It exists so that a connection does not. A device's connection to the daemon stands for as long as
// the device is switched on, and carries everything — chat, approvals, reminders — while a spoken
// exchange lasts a minute and happens many times over that connection's life. Hanging the session
// off the connection would have made those two lifetimes the same one, and would have given the
// connection state it deliberately does not have.
//
// Unlike chat sessions, a voice session is NOT server-owned past its device. A chat turn survives a
// disconnect because its answer is still worth having when the client returns; audio is worth
// nothing with nobody to play it to, and a live model bills for every second of it. So a session
// ends when its device goes away — which is a decision recorded here, not a consequence of where an
// object happened to hang.
//
// No context is stored (Go: don't put a ctx in a struct). Each session keeps its own cancel and the
// Manager tracks it; CloseAll is the single global stop.
type Manager struct {
	driver *Driver
	log    *slog.Logger

	mu     sync.Mutex
	active map[string]*session // by device id
	wg     sync.WaitGroup      // one per running session; CloseAll waits so none outlives shutdown
	closed bool
}

// Sink is the writing half of a device: where a session's speech and its one control signal go.
//
// A caller supplies only this, never a whole Device, because only the writing half is something it
// owns. The reading half belongs to the session — a standing connection outlives many conversations,
// so "the next chunk of microphone audio" means nothing without knowing which conversation is
// asking. The Manager therefore holds the microphone side itself and hands the driver a Device that
// joins the two.
type Sink interface {
	// Play queues one chunk of speech for the device.
	Play(pcm []byte) error
	// Interrupt tells the device to drop whatever it has buffered but not yet played.
	Interrupt() error
	// Waiting reports that the conversation is blocked on a human decision somewhere else — or that
	// it no longer is.
	//
	// The device cannot work this out: an approval is happening on a phone it knows nothing about,
	// and from here the conversation merely goes quiet. Everything else a satellite shows it derives
	// from what it observed itself; this is the one state only the daemon can see.
	Waiting(on bool) error
}

// errSessionOver ends a session's Recv without implying the device went away. The driver treats a
// Recv error as the end of the conversation, which is exactly right here — and the connection
// underneath is untouched, ready for the next wake word.
var errSessionOver = errors.New("voice: session ended")

// errHungUp unwinds Run when the model calls hang_up. It is a normal ending, not a failure, and Run
// converts it back to a nil error — a caller that logged this as a fault would be reporting good
// manners as a bug.
var errHungUp = errors.New("voice: the model hung up")

// session is one running conversation: the handle that stops it, and the microphone queue the
// connection feeds.
type session struct {
	cancel context.CancelFunc
	done   chan struct{}
	mic    chan []byte
	// The device's own detector reporting a voice. Depth one and never blocking: what matters is
	// THAT someone spoke, not how many times, and the only consumer resets a timer on it.
	heard chan struct{}
}

// deviceView is the Device the driver sees: a caller's Sink for writing, this session's queue for
// reading. It exists for exactly one conversation, while the connection behind the Sink stands for
// as long as the device is switched on.
type deviceView struct {
	Sink
	mic   chan []byte
	heard chan struct{}
	done  chan struct{}
}

// Heard is the device's own voice detector, not the model's transcript.
//
// It arrives from the board within about 200 ms of someone starting to speak, against the model's
// transcript which only appears once something has been UNDERSTOOD. For deciding whether anyone is
// still there, the difference is the whole point.
func (d *deviceView) Heard() <-chan struct{} { return d.heard }

// Recv returns the next microphone chunk, or ends the session.
//
// A closed session reports errSessionOver rather than blocking: the driver's pump would otherwise
// sit on a queue nobody fills, and Run would only unwind when its budget expired.
func (d *deviceView) Recv(ctx context.Context) ([]byte, error) {
	select {
	case pcm := <-d.mic:
		return pcm, nil
	case <-d.done:
		return nil, errSessionOver
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Ended is called when a session finishes, with whatever was said. The Manager does not persist the
// transcript itself: where a spoken conversation belongs — a chat store, a log, nowhere — is the
// consumer's decision, the same way agentkit leaves persistence to its consumer.
type Ended func(deviceID string, transcript []agentkit.Message, err error)

// NewManager builds a Manager over the driver every session runs on.
func NewManager(d *Driver, log *slog.Logger) *Manager {
	return &Manager{driver: d, log: log.With("component", "voice"), active: map[string]*session{}}
}

// Start opens a conversation for deviceID over dev, seeded with conv, and returns once it is
// running. Starting one for a device that already has one replaces it: a wake word during a
// conversation means the person is addressing the assistant again, not that they want two.
//
// ended is called from the session's own goroutine when it finishes, however it finishes.
// who reports the current speaker, and is consulted rather than captured: recognition keeps running
// while the conversation does. Nil means no microphone identifies anybody, which is every caller
// without speaker profiles behind it.
func (m *Manager) Start(deviceID string, out Sink, conv []agentkit.Message, who func() speaker.Identity, ended Ended) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if prev := m.active[deviceID]; prev != nil {
		m.mu.Unlock()
		m.log.Info("restarting voice session", "device", deviceID)
		m.stop(deviceID)
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
	}

	// Background, not the context of the connection that asked. A device holds two connections while a
	// dropped one times out, and it says the wake word again on the new one during exactly that window
	// — so a session parented on a connection would be cancelled by the OTHER connection's teardown,
	// moments after it started and often while the provider was still setting it up. The lifetime here
	// is the Manager's to end, and Stop/CloseAll are the only two things that may end it.
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{
		cancel: cancel,
		done:   make(chan struct{}),
		// A second or so of speech. Deeper only hides a link that cannot keep up, and stale
		// microphone audio is worth less than knowing the link is congested.
		mic:   make(chan []byte, 48),
		heard: make(chan struct{}, 1),
	}
	dev := &deviceView{Sink: out, mic: s.mic, heard: s.heard, done: s.done}
	m.active[deviceID] = s
	m.wg.Add(1)
	m.mu.Unlock()

	m.log.Info("voice session started", "device", deviceID)
	go func() {
		defer m.wg.Done()
		defer close(s.done)
		transcript, err := m.driver.Run(ctx, dev, conv, who)
		cancel() // release the context even when Run returned on its own

		m.mu.Lock()
		// Only clear the entry if it is still ours: a restart may already have replaced it, and
		// deleting blindly would leave the newer session untracked and unstoppable.
		if m.active[deviceID] == s {
			delete(m.active, deviceID)
		}
		m.mu.Unlock()

		m.log.Info("voice session ended", "device", deviceID, "messages", len(transcript), "err", err)
		if ended != nil {
			ended(deviceID, transcript, err)
		}
	}()
}

// Stop ends the device's session and waits for it to unwind. It is a no-op for a device with none.
//
// Waiting matters: the driver cancels a pending tool call as it tears down, and returning before
// that has happened would let an approval still sitting on somebody's phone answer a call that no
// longer exists.
func (m *Manager) Stop(deviceID string) { m.stop(deviceID) }

func (m *Manager) stop(deviceID string) {
	m.mu.Lock()
	s := m.active[deviceID]
	delete(m.active, deviceID)
	m.mu.Unlock()
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
}

// Feed hands one chunk of microphone audio to the device's running conversation, dropping it if
// there is none or the queue is full.
//
// Dropping rather than blocking is the same reasoning as everywhere else on this path: the caller is
// a connection's read loop, and a read loop that waits stops serving everything else the device
// sends. A dropped frame costs a click; a stalled read loop costs the connection.
func (m *Manager) Feed(deviceID string, pcm []byte) {
	m.mu.Lock()
	s := m.active[deviceID]
	m.mu.Unlock()
	if s == nil {
		return // no conversation is listening; the device is streaming into nothing
	}
	select {
	case s.mic <- pcm:
	default:
		m.log.Debug("microphone frame dropped", "device", deviceID)
	}
}

// Heard reports that the device's detector picked up a voice. Dropped if one is already pending:
// the consumer only ever resets a timer, and two resets are the same as one.
func (m *Manager) Heard(deviceID string) {
	m.mu.Lock()
	s := m.active[deviceID]
	m.mu.Unlock()
	if s == nil {
		return
	}
	select {
	case s.heard <- struct{}{}:
	default:
	}
}

// Active reports whether the device has a conversation running.
func (m *Manager) Active(deviceID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[deviceID] != nil
}

// CloseAll ends every session and waits. After it, Start is a no-op — a manager that accepted a new
// session during shutdown would hold the daemon open on a conversation nobody can hear.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	sessions := make([]*session, 0, len(m.active))
	for id, s := range m.active {
		sessions = append(sessions, s)
		delete(m.active, id)
	}
	m.mu.Unlock()

	m.log.Info("closing all voice sessions", "count", len(sessions))
	for _, s := range sessions {
		s.cancel()
	}
	m.wg.Wait()
}
