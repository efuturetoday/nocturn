package serve

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/voice"
)

// The voice domain: speech rides this connection beside the tagged JSON rather than getting an
// endpoint of its own.
//
// /ws is already a multiplexer — one connection per device carrying chat, agents, approvals and
// reminders — so a second socket would need a second copy of every rule that hangs off a device's
// connection: the approval broker attach, the hub, last-used, presence. It would also mean the phone
// needing another connection later to do what a satellite does, since voice is a domain on the
// protocol it already speaks.
//
// Control is JSON like everything else; the audio itself is binary frames, which are the only frames
// on this socket that are not commands.

// VoiceWake asks for a spoken session on this device. It carries no audio: the frames that follow do.
type VoiceWake struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// VoiceEnd closes the spoken session. It is the polite path, not the only one: a session also ends
// when the device's last connection goes, when the provider closes its stream, and in the last resort
// when the driver's wall-clock budget expires. There is no inactivity timer of our own — a device that
// falls silent without disconnecting is the provider's turn-taking to notice.
type VoiceEnd struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// VoiceState reports what the session is doing, so a device can show it without inferring. Sent on
// every transition.
type VoiceState struct {
	Type  string `json:"type"` // always "voice.state"
	Ws    string `json:"ws"`
	State string `json:"state"` // "listening" | "idle"
}

// VoiceCredit is the device saying how many more bytes of speech it can hold.
//
// It is the only flow control on the downlink, and it replaces waiting for a socket to accept a
// write. The device grants its whole playout buffer when a session opens and grants more as playback
// consumes it; the daemon sends only what it has been granted. Overflow becomes structurally
// impossible rather than merely unlikely.
//
// The alternative — blocking the write until the device's socket accepts it — was built and
// measured. The device blocks in its own receive callback while its buffer is full, and that
// callback runs on the task driving the connection's keepalive: a wait longer than the client's read
// timeout looks to it like a dead socket, and it answers by dropping the connection. Flow control
// that kills the link under load is not flow control.
type VoiceCredit struct {
	Cmd   string `json:"cmd"`
	Ws    string `json:"ws"`
	Bytes int    `json:"bytes"`
}

// VoiceInterrupt tells the device to drop what it has buffered but not yet played — the barge-in
// signal. It is the one control message the audio path itself produces.
type VoiceInterrupt struct {
	Type string `json:"type"` // always "voice.interrupt"
}

// voice routes the voice domain.
func (c *conn) voice(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "voice.wake":
		var m VoiceWake
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad voice.wake")
			return
		}
		c.voiceWake(ctx, m)
	case "voice.end":
		var m VoiceEnd
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad voice.end")
			return
		}
		c.voiceEnd(ctx, m)
	case "voice.credit":
		var m VoiceCredit
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad voice.credit")
			return
		}
		if s := sinkOf(c.device); s != nil {
			s.grant(m.Bytes)
		}
	case "voice.speech":
		// The device's detector heard someone. It carries nothing but the fact, which is all a
		// silence timer needs, and it is deliberately not audio: audio says what was said, this says
		// somebody is still there.
		for _, ws := range c.spaces {
			if sessions := ws.VoiceSessions(); sessions != nil && sessions.Active(c.device) {
				sessions.Heard(c.device)
				return
			}
		}
	default:
		c.badRequest(ctx, "unknown voice command: "+cmd)
	}
}

// The sinks with an open session, by device.
//
// A credit arrives on the connection but belongs to the SESSION, and the session outlives any one
// socket — a device that reconnects mid-conversation carries on over the new one. Routing the grant
// through the device rather than the connection is what keeps that true.
var (
	sinksMu sync.Mutex
	sinks   = map[string]*deviceSink{}
)

func sinkOf(device string) *deviceSink {
	sinksMu.Lock()
	defer sinksMu.Unlock()
	return sinks[device]
}

func (c *conn) voiceWake(ctx context.Context, m VoiceWake) {
	ws, ok := c.workspace(ctx, m.Ws)
	if !ok {
		return
	}
	sessions := ws.VoiceSessions()
	if sessions == nil {
		// No live model is configured in this process. Saying so beats opening a session that can
		// never answer and leaving the device waiting for speech that will not come.
		c.badRequest(ctx, "voice is not configured on this daemon")
		return
	}

	device := c.device
	hub := c.hub
	// Recognition starts with the session and runs beside it. Nothing waits for it: by the time
	// anything asks who is talking, seconds of speech have gone past.
	c.listen = newListener(c.embedder, ws.Voices(), c.log)
	sink := newDeviceSink(hub, device, c.log)
	sinksMu.Lock()
	sinks[device] = sink
	sinksMu.Unlock()

	// Nothing is seeded. A spoken exchange starts fresh rather than resuming whatever was last said,
	// because the person at a speaker has no way to see what it thinks the context is.
	sessions.Start(device, sink, nil, c.listen.who, func(_ string, transcript []agentkit.Message, err error) {
		held, blocked := sink.stats()
		sink.close()
		c.log.Info("voice session finished", "messages", len(transcript),
			"undelivered_ms", held, "blocked_ms", blocked, "err", err)
		c.capture.flush(time.Now())
		hub.control(device, VoiceState{Type: "voice.state", Ws: m.Ws, State: "idle"})
	})
	c.send(ctx, VoiceState{Type: "voice.state", Ws: m.Ws, State: "listening"})
}

func (c *conn) voiceEnd(ctx context.Context, m VoiceEnd) {
	ws, ok := c.workspace(ctx, m.Ws)
	if !ok {
		return
	}
	if sessions := ws.VoiceSessions(); sessions != nil {
		sessions.Stop(c.device)
	}
	c.capture.flush(time.Now())
}

// audioIn hands a binary frame to whichever workspace has a session open for this device.
//
// The frame carries no workspace of its own: a device has one conversation at a time, so the
// alternative would be a routing header on every twenty-millisecond chunk to say something the wake
// word already said.
func (c *conn) audioIn(pcm []byte) {
	// Recording is deliberately ahead of the session lookup, because the case it exists for has no
	// session: enrolment opens no conversation, so audio that arrives during one would otherwise fall
	// straight through to the comment at the bottom. Created here rather than at accept so only a
	// device that actually streams a microphone gets a recorder — and this runs on the read loop,
	// which is one goroutine per connection.
	if c.capture == nil {
		c.capture = newCapture(c.device, c.log)
	}
	c.capture.add(pcm, time.Now())
	c.listen.add(pcm)

	for _, ws := range c.spaces {
		if sessions := ws.VoiceSessions(); sessions != nil && sessions.Active(c.device) {
			sessions.Feed(c.device, pcm)
			return
		}
	}
	// Audio with no session open: the device is streaming into nothing, which is the normal shape of
	// an enrolment recording and also what a cancelled wake word leaves in flight.
}

// endVoice closes this device's session when the connection going away was its last.
//
// A session belongs to the DEVICE, not to the socket that opened it, and the reason is narrower than
// it looks. It is NOT that a conversation survives a reconnect: the microphone stops mid-sentence, the
// provider reads the gap as the end of a turn, and the reply goes into a socket nobody is reading.
// What survives such a gap is the session object, not the exchange.
//
// It is that a device holds two connections for as long as a dead one takes to time out — and that is
// precisely the moment it says the wake word again. A session started on the live connection would
// then be cancelled by the dead one's teardown, often while the provider was still setting it up. The
// conversation being protected is the NEW one, not one carried across the gap.
//
// Beyond that the tie to the connection is deliberate: audio is worth nothing with nobody to play it
// to, and a live model bills for every second. Chat sessions survive the same event, because their
// answer is still worth having later.
func (c *conn) endVoice() {
	// serve removes this connection from the hub before calling here, so what is left is what the
	// device still has.
	if c.hub.countOf(c.device) > 0 {
		return
	}
	for _, ws := range c.spaces {
		if sessions := ws.VoiceSessions(); sessions != nil {
			sessions.Stop(c.device)
		}
	}
	c.capture.flush(time.Now())
}

// maxBacklog bounds what may pile up waiting for a device that has stopped taking speech. Roughly a
// minute of it.
//
// It is not a latency budget — audio leaves as fast as the device will take it — but a ceiling on a
// model that will not stop talking to a device that has stopped listening. Reaching it ends the
// session: a minute of undelivered speech means the device is gone, and a live model bills for every
// second it is left open.
const maxBacklog = 16000 * 2 * 60

// downFrameBytes is one frame to the device: 20 ms at 16 kHz mono PCM16. Header overhead is around
// seven percent there, which is the point past which larger frames buy latency and nothing else.
const downFrameBytes = 16000 * 2 * 20 / 1000

// deviceSink is the writing half of a device: where a session's speech and its barge-in signal go.
//
// It addresses the device rather than the connection that started the session, so a reconnect
// mid-conversation reaches the new socket. The session itself is unaware either way.
//
// THE RATE IS TCP'S PROBLEM, and this is the design's whole load-bearing decision, arrived at after
// building the alternative and measuring it fail. The satellite blocks in its own receive callback
// while its playback buffer is full; that callback runs on the task that reads the socket, so lwIP
// stops being drained, the receive window closes, and the write below blocks until the speaker has
// made room. Flow control end to end, in the one implementation on this path that is already correct.
//
// The alternative was an application-level credit window — the device reporting what it had played,
// the sender allowed that much more. It works on paper and starves in practice: what the sender may
// have outstanding covers both what is BUFFERED on the device and what is IN FLIGHT toward it, so the
// buffer that actually exists is the window minus the loop's round trip. Measured here: 208 ms of
// round trip against a 256 ms window left 48 ms to play from, the speaker ran dry fifty times a
// second, and no amount of tuning the two numbers fixed it because they are the same number. TCP has
// no such subtraction — it stops the sender when the buffer is genuinely full, not when a report says
// it might be.
//
// What is paid for it is barge-in reach. Speech already inside a TCP buffer cannot be recalled, so an
// interruption cannot unsay it. The answer is to keep every queue on this path SHORT — the backlog
// here is discarded on Interrupt, the socket queue is four frames, and the device's receive window is
// deliberately small — rather than to invent a protocol that can take audio back.
type deviceSink struct {
	hub    *hub
	device string
	log    *slog.Logger

	mu      sync.Mutex
	backlog []byte
	// How many bytes the device has said it can still take. Nothing is sent without it.
	credit int
	wake   chan struct{}
	done   chan struct{}
	// Instrumentation: what was still held when the session ended, and how long the writer spent
	// blocked on a device that would not take more. The second is the honest measure of whether the
	// link can carry the conversation.
	blockedTotal time.Duration
}

var _ voice.Sink = (*deviceSink)(nil)

// errDeviceGone ends a session whose speech nobody is taking. The driver treats a Play error as the
// end of the conversation, which is the right answer: the alternative is a live model billing into a
// backlog that is only growing.
var errDeviceGone = errors.New("the device stopped taking speech")

func newDeviceSink(h *hub, device string, log *slog.Logger) *deviceSink {
	s := &deviceSink{hub: h, device: device, log: log,
		wake: make(chan struct{}, 1), done: make(chan struct{})}
	go s.run()
	return s
}

// Play accepts a chunk of speech and returns immediately.
//
// It must never block, and the reason is not throughput. This runs on the driver's event loop, the
// same goroutine that delivers the model's interruptions and its tool calls — so a Play that waited
// for the speaker would delay the barge-in signal by exactly as long as it waited, which is to say
// the interruption would arrive after the audio it was meant to cancel. The waiting belongs on a
// goroutine that has nothing else to do.
func (s *deviceSink) Play(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.backlog) >= maxBacklog {
		s.log.Warn("voice backlog full — ending the session", "device", s.device,
			"backlog_ms", int64(len(s.backlog))*1000/(16000*2))
		return errDeviceGone
	}
	s.backlog = append(s.backlog, downsample24to16(pcm)...)
	select {
	case s.wake <- struct{}{}:
	default: // already awake; it will see the new bytes
	}
	return nil
}

// grant adds to what the device says it can take, and wakes the writer if it was waiting on exactly
// this.
func (s *deviceSink) grant(bytes int) {
	if bytes <= 0 {
		return
	}
	s.mu.Lock()
	s.credit += bytes
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// run moves the backlog onto the device's socket, as fast as credit allows and no faster.
//
// It never blocks on the socket. What it waits for is a grant, which is the device reporting that
// its speaker has consumed something — the one clock in this system that is real, since it is the
// codec's crystal. Nothing here is paced by a timer, and there is no rate to get wrong: audio leaves
// at exactly the speed the far speaker plays it.
func (s *deviceSink) run() {
	for {
		select {
		case <-s.done:
			return
		case <-s.wake:
		}
		for {
			s.mu.Lock()
			// A frame is sent whole or not at all. Splitting one to fit the remaining credit would
			// mean the device receiving a fragment it cannot align, for no gain — the rest of the
			// credit arrives a hundred milliseconds later against a two second buffer.
			if len(s.backlog) == 0 || s.credit < downFrameBytes {
				s.mu.Unlock()
				break
			}
			n := min(len(s.backlog), downFrameBytes)
			frame := make([]byte, n)
			copy(frame, s.backlog[:n])
			s.backlog = s.backlog[n:]
			s.credit -= n
			s.mu.Unlock()

			start := time.Now()
			delivered := s.hub.sendAudio(s.device, frame, s.done)
			if waited := time.Since(start); waited > time.Millisecond {
				s.mu.Lock()
				s.blockedTotal += waited
				s.mu.Unlock()
			}
			if !delivered {
				// Either the session is over or the device has no connection at all. Dropping the frame
				// is right in both cases: a device that is not there has no buffer to fill, and audio
				// held for one that reconnects later would be answering a question long since dropped.
				continue
			}
		}
	}
}

// close stops the writer. Safe to call once, from the session's teardown.
func (s *deviceSink) close() { close(s.done) }

// Interrupt discards everything not yet heard that can still be reached.
//
// Three places hold speech and this reaches the two it can: the backlog here, and the frames queued
// on the socket but not yet written. What is already inside a TCP buffer is gone — it will arrive and
// the device will play it — which is why the device is told to flush as well, and why every queue on
// this path is kept short. The control message travels on its own queue and overtakes audio, so it
// lands before whatever survives.
func (s *deviceSink) Interrupt() error {
	// The credit stays: it counts room in the device's queue, and discarding our backlog changes
	// that room not at all. The device's flush credits back what it actually freed.
	s.mu.Lock()
	s.backlog = s.backlog[:0]
	s.mu.Unlock()

	s.hub.dropAudio(s.device)
	s.hub.control(s.device, VoiceInterrupt{Type: "voice.interrupt"})
	return nil
}

// stats reports, in milliseconds, what was never delivered and how long the writer spent waiting on
// the device.
func (s *deviceSink) stats() (undelivered, blocked int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.backlog)) * 1000 / (16000 * 2), s.blockedTotal.Milliseconds()
}

// downsample24to16 converts the model's 24 kHz output to the 16 kHz a satellite runs at.
//
// It happens here rather than on the device because the ratio is exactly 3:2 and this side has the
// CPU to spare — a board's two cores are already carrying an audio front end, and a stall there is
// what breaks its echo cancellation. Three input samples become two output samples by linear
// interpolation, which for speech is indistinguishable from anything more careful.
func downsample24to16(pcm []byte) []byte {
	const inRate, outRate = 24000, 16000

	in := len(pcm) / 2
	if in < 2 {
		return pcm
	}
	sample := func(i int) int32 { return int32(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8)) }

	out := in * outRate / inRate
	res := make([]byte, out*2)
	for j := range out {
		// Position in the input, in 1/1000ths of a sample, to keep this integer-only.
		pos := j * inRate * 1000 / outRate
		i, frac := pos/1000, int32(pos%1000)
		next := i + 1
		if next >= in {
			next = in - 1
		}
		v := sample(i) + (sample(next)-sample(i))*frac/1000
		res[2*j] = byte(uint16(v))
		res[2*j+1] = byte(uint16(v) >> 8)
	}
	return res
}
