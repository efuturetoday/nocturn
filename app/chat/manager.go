package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/app/tools"
)

// idleTTL: a live session with no running turn is unloaded this long after its last turn ended. It
// reloads from the store on the next open — bounding memory without ever touching a running turn.
const idleTTL = 10 * time.Minute

// Manager owns a workspace's LIVE chat sessions, keyed by chat id. A session is SERVER-owned: it runs
// on context.Background() (never a request/connection ctx) and lives until it is explicitly closed —
// so a turn survives client disconnects, chat switches and reconnects. A connection never owns or
// closes a session; it only observes the event stream the Manager forwards (see OnEvent). Idle
// sessions are reaped after idleTTL; a session with a running turn is never reaped. This is the
// live-session lifecycle agentkit deliberately leaves to the consumer (see agentkit WithStore).
//
// No context.Context is stored (Go: don't put a ctx in a struct) — the session keeps its own cancel,
// and the Manager tracks + closes; CloseAll is the single global stop.
type Manager struct {
	rt    *runtime.Runtime
	store *Store

	mu     sync.Mutex
	active map[string]*live // by chat id
	emit   func(chatID string, ev agentkit.Event)
	wg     sync.WaitGroup // one per running pump; CloseAll waits so no session outlives shutdown
	stop   chan struct{}  // closed once by CloseAll to stop the reaper
	closed bool
}

// live is one running/idle session plus the idle bookkeeping the reaper reads.
type live struct {
	sess      *agentkit.Session
	running   bool
	idleSince time.Time
}

// NewManager builds a Manager over a Runtime and a Store and starts its idle-session reaper. Call
// CloseAll on shutdown to stop the reaper and close every live session.
func NewManager(rt *runtime.Runtime, store *Store) *Manager {
	m := &Manager{rt: rt, store: store, active: map[string]*live{}, stop: make(chan struct{})}
	go m.reap()
	return m
}

// OnEvent registers the sink every live session's events are forwarded to (chat id + event) — the
// server broadcasts them to connected clients. Set once, at wiring time, before serving.
func (m *Manager) OnEvent(fn func(chatID string, ev agentkit.Event)) {
	m.mu.Lock()
	m.emit = fn
	m.mu.Unlock()
}

// Open returns the live session for id — the SAME one if it is already live (so a running turn is
// shared, never duplicated), else a fresh session (transcript loaded from the store) whose pump is
// started. It never submits.
func (m *Manager) Open(id string) *agentkit.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openLocked(id)
}

func (m *Manager) openLocked(id string) *agentkit.Session {
	if lv := m.active[id]; lv != nil {
		return lv.sess
	}
	// Background, not a request ctx: the session outlives any connection; only Close ends it. The
	// chat id is stamped on the ctx so the wake tool can resume THIS chat by id — a plain tag, no
	// resume logic here (the manager stays wake-agnostic).
	ctx := tools.WithChatID(context.Background(), id)
	lv := &live{sess: m.rt.Session(ctx, agentkit.WithStore(m.store, id)), idleSince: time.Now()}
	m.active[id] = lv
	m.wg.Add(1)
	go m.pump(id, lv)
	return lv.sess
}

// Start mints a new chat id and submits its first message on a fresh live session.
func (m *Manager) Start(text string) (string, *agentkit.Session) {
	id := NewID()
	m.mu.Lock()
	sess := m.openLocked(id)
	m.mu.Unlock()
	sess.Submit(text)
	return id, sess
}

// Cancel aborts the RUNNING turn of a live chat (the stop button) — turn-scoped: the session stays
// open for the next message. A no-op for a chat that is not live.
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	lv := m.active[id]
	m.mu.Unlock()
	if lv != nil {
		lv.sess.Cancel()
	}
}

// pump drains one session's event stream — ALWAYS, whether or not a client is watching, so a turn
// never blocks on a full channel — tracks the running/idle state, captures the tool forest, and
// forwards each event to emit. It exits when the session is closed (Delete / reaper / CloseAll), which
// closes the stream.
func (m *Manager) pump(id string, lv *live) {
	defer m.wg.Done()
	m.mu.Lock()
	emit := m.emit
	m.mu.Unlock()
	f := newForest() // pump-local: one goroutine owns it, so no lock; a reload spins a fresh one
	for ev := range lv.sess.Subscribe() {
		m.track(lv, ev)
		m.recordForest(id, f, ev)
		if emit != nil {
			emit(id, ev)
		}
	}
}

// recordForest builds the turn's tool forest from the SAME event stream the pump drains — the parent
// linkage (ToolStart.Frame → the enclosing call id) exists only here and on the wire, never in the
// transcript. On the turn's close (TurnEnd, Frame 0) it flushes the group to the store so a reopened
// chat can rebuild the nesting, including nested host-bridge and sub-agent calls (non-zero frames)
// that never reach the transcript. Best-effort: a persist error is swallowed (observability, not
// authority), like the event sink itself.
func (m *Manager) recordForest(id string, f *forest, ev agentkit.Event) {
	switch e := ev.(type) {
	case agentkit.TurnStart:
		if e.Frame == 0 {
			f.reset()
		}
	case agentkit.ToolStart:
		f.start(e.ID, e.Frame, e.Tool, e.Args)
	case agentkit.ToolEnd:
		f.end(e.ID, e.Result, errText(e.Err), e.Duration.Milliseconds())
	case agentkit.TurnEnd:
		if e.Frame == 0 {
			_ = m.store.AppendTools(id, f.snapshot())
			f.reset()
		}
	}
}

// Tools returns a chat's persisted per-turn tool forest, for rebuilding the nested forest on snapshot.
func (m *Manager) Tools(id string) ([][]ToolNode, error) { return m.store.LoadTools(id) }

// errText renders an error as a wire string ("" for nil) — the store keeps a captured tool's error as
// text, mirroring the live ToolEnd wire form.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// forest accumulates one turn's tool calls from the live event stream, keyed by call id, in first-seen
// (start) order so parents precede children. It is owned by a single pump goroutine — no locking.
type forest struct {
	order []uint64
	nodes map[uint64]*ToolNode
}

func newForest() *forest { return &forest{nodes: map[uint64]*ToolNode{}} }

// start records a call's opening (create-if-absent); parent is the enclosing call id (0 = top level).
func (f *forest) start(id, parent uint64, tool, args string) {
	if _, ok := f.nodes[id]; ok {
		return
	}
	f.nodes[id] = &ToolNode{ID: id, Parent: parent, Tool: tool, Args: args}
	f.order = append(f.order, id)
}

// end fills in a call's result once it returns. A missing start (should not happen) is ignored.
func (f *forest) end(id uint64, result, errText string, durationMs int64) {
	if n := f.nodes[id]; n != nil {
		n.Result, n.Err, n.DurationMs = result, errText, durationMs
	}
}

// snapshot returns the turn's nodes in start order (parents before children).
func (f *forest) snapshot() []ToolNode {
	out := make([]ToolNode, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, *f.nodes[id])
	}
	return out
}

func (f *forest) reset() {
	f.order = f.order[:0]
	clear(f.nodes)
}

// track keeps a live chat's running/idle state current from its own event stream — only the
// top-level turn (Frame 0) flips it; sub-agent frames are part of that same turn.
func (m *Manager) track(lv *live, ev agentkit.Event) {
	switch e := ev.(type) {
	case agentkit.TurnStart:
		if e.Frame == 0 {
			m.mu.Lock()
			lv.running = true
			m.mu.Unlock()
		}
	case agentkit.TurnEnd:
		if e.Frame == 0 {
			m.mu.Lock()
			lv.running = false
			lv.idleSince = time.Now()
			m.mu.Unlock()
		}
	}
}

// reap unloads idle sessions past idleTTL until CloseAll stops it.
func (m *Manager) reap() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.reapIdle()
		}
	}
}

func (m *Manager) reapIdle() {
	now := time.Now()
	var dead []*agentkit.Session
	m.mu.Lock()
	for id, lv := range m.active {
		if !lv.running && now.Sub(lv.idleSince) > idleTTL {
			dead = append(dead, lv.sess)
			delete(m.active, id) // remove FIRST so a concurrent Open spins a fresh one
		}
	}
	m.mu.Unlock()
	for _, s := range dead {
		s.Close() // closes the stream → its pump exits
	}
}

// List returns every chat's metadata, most recent first.
func (m *Manager) List() ([]Meta, error) { return m.store.Metas() }

// Transcript returns a chat's persisted messages, for rendering a snapshot on open.
func (m *Manager) Transcript(id string) ([]agentkit.Message, error) { return m.store.Load(id) }

// Delete stops a chat's live session (if any) and removes its transcript.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	lv := m.active[id]
	delete(m.active, id)
	m.mu.Unlock()
	if lv != nil {
		lv.sess.Close()
	}
	return m.store.Delete(id)
}

// CloseAll stops the reaper and closes every live session, then waits for their pumps to drain — the
// single global stop, called once on daemon shutdown. Idempotent.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.stop)
	sessions := make([]*agentkit.Session, 0, len(m.active))
	for id, lv := range m.active {
		sessions = append(sessions, lv.sess)
		delete(m.active, id)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
	m.wg.Wait()
}

// NewID mints a short random chat id. A crypto/rand failure would yield a colliding all-zero id, so
// it panics instead — the failure is catastrophic and near-impossible, not a case to paper over.
func NewID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic("chat: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
