package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
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
	resolve func(id string) *runtime.Runtime // the runtime a chat id spins under (see NewManager)
	store   *Store
	log     *slog.Logger

	mu     sync.Mutex
	active map[string]*live // by chat id
	emit   func(chatID string, ev agentkit.Event)
	wg     sync.WaitGroup // one per running pump; CloseAll waits so no session outlives shutdown
	stop   chan struct{}  // closed once by CloseAll to stop the reaper
	closed bool
}

// live is one running/idle session plus the idle bookkeeping the reaper reads AND the IN-FLIGHT turn
// state, so a client that reopens the chat mid-turn can be handed the running turn instead of a stale
// transcript that ends before it. The daemon does NOT materialize a render model of the running turn:
// it buffers the turn's raw events and replays them, so the answer/reasoning/tool nesting are folded
// by the ONE client-side reducer that also drives the live stream (no server-side second fold to keep
// in sync). `forest` stays because it structures the turn's tool calls for PERSISTENCE at close (an
// observability artifact beside the transcript), not to render. All fields are guarded by Manager.mu.
type live struct {
	sess      *agentkit.Session
	running   bool
	idleSince time.Time

	// in-flight turn (guarded by Manager.mu); cleared on TurnEnd(frame 0)
	input  string           // the current turn's user message — not yet persisted, not a stream event
	events []agentkit.Event // the turn's events so far, replayed to a client reopening mid-turn
	forest *forest          // the turn's tool calls (nested) — for PERSISTING the forest group at close
}

// NewManager builds a Manager over a per-chat runtime resolver and a Store and starts its
// idle-session reaper. resolve reports which runtime a chat id spins under: a user manager returns a
// constant (the workspace root runtime); an agent manager returns the owning agent's runtime (keyed
// by the run's Meta.Agent), which is what lets one agent run per its own cage/gate/persona. Call
// CloseAll on shutdown to stop the reaper and close every live session.
func NewManager(resolve func(id string) *runtime.Runtime, store *Store, log *slog.Logger) *Manager {
	m := &Manager{resolve: resolve, store: store, log: log.With("component", "chat"), active: map[string]*live{}, stop: make(chan struct{})}
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
	lv := &live{sess: m.resolve(id).Session(ctx, agentkit.WithStore(m.store, id)), idleSince: time.Now()}
	m.active[id] = lv
	m.wg.Add(1)
	go m.pump(id, lv)
	m.log.Debug("session opened", "chat", id)
	return lv.sess
}

// Start mints a new chat id and submits its first message on a fresh live session.
func (m *Manager) Start(text string) (string, *agentkit.Session) {
	id := NewID()
	m.mu.Lock()
	sess := m.openLocked(id)
	m.mu.Unlock()
	m.log.Info("chat started", "chat", id)
	m.touch(id, text)
	sess.Submit(text)
	return id, sess
}

// Submit sends text to chat id (opening it if needed) and records the input as the in-flight turn's
// user message, so a client that reopens before the turn ends still sees the message and the running
// state (the transcript only gets it at TurnEnd). This is the server path; Start is the REPL path.
func (m *Manager) Submit(id, text string) {
	m.mu.Lock()
	sess := m.openLocked(id)
	if lv := m.active[id]; lv != nil {
		lv.input = text
	}
	m.mu.Unlock()
	m.touch(id, text)
	sess.Submit(text)
}

// Fire starts an agent run: it stamps the run's owner (so the runtime resolver spins it under that
// agent's runtime), opens a fresh live session, and submits the task — after which the run behaves
// exactly like any chat (its pump persists the transcript, builds the forest, and streams events to
// OnEvent). It is fire-and-forget: the run outlives the caller (the session runs on the manager's own
// background ctx), so a scheduled or manually-triggered run is unaffected by a connection closing.
// Intended for the agent manager; agentName must be the run's owning agent.
func (m *Manager) Fire(id, agentName, task string) {
	if err := m.store.SetOwner(id, agentName); err != nil {
		m.log.Error("agent run: stamping owner failed", "chat", id, "agent", agentName, "err", err)
		return
	}
	m.mu.Lock()
	sess := m.openLocked(id) // resolver now sees the owner via OwnerOf
	if lv := m.active[id]; lv != nil {
		lv.input = task
	}
	m.mu.Unlock()
	m.log.Info("agent run fired", "chat", id, "agent", agentName)
	m.touch(id, task)
	sess.Submit(task)
}

// touch bumps the chat's list metadata (Updated + Preview) from the submitted text so the chat rises
// in every device's list the moment it is sent — not only when the turn ends. Best-effort: a failure
// here must not scuttle the turn (the transcript persists at close regardless), so it is logged and
// swallowed. Called OUTSIDE m.mu — Store.Touch takes its own lock and broadcasts the activity.
func (m *Manager) touch(id, text string) {
	if err := m.store.Touch(id, text); err != nil {
		m.log.Warn("chat touch failed", "chat", id, "err", err)
	}
}

// Cancel aborts the RUNNING turn of a live chat (the stop button) — turn-scoped: the session stays
// open for the next message. A no-op for a chat that is not live.
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	lv := m.active[id]
	m.mu.Unlock()
	if lv != nil {
		m.log.Info("turn cancelled", "chat", id)
		lv.sess.Cancel()
	}
}

// pump drains one session's event stream — ALWAYS, whether or not a client is watching, so a turn
// never blocks on a full channel — buffers the in-flight turn's events (see observe), flushes the
// tool forest at each turn's close, and forwards each event to emit. It exits when the session is
// closed (Delete / reaper / CloseAll), which closes the stream.
func (m *Manager) pump(id string, lv *live) {
	defer m.wg.Done()
	m.mu.Lock()
	emit := m.emit
	m.mu.Unlock()
	for ev := range lv.sess.Subscribe() {
		m.observe(id, lv, ev)
		if emit != nil {
			emit(id, ev)
		}
	}
}

// observe maintains the live session's in-flight state from its OWN event stream. Only the top-level
// turn (Frame 0) opens/closes the turn; sub-agent frames belong to it. It does NOT interpret the
// stream into a render model: it buffers the running turn's raw events (any frame) so a reopening
// client replays them through the same client-side fold as the live stream. It also structures the
// tool calls into `forest` — for PERSISTENCE only. On TurnEnd(frame 0) it flushes the forest group to
// the store (best-effort: a persist error is swallowed — observability, not authority) and clears the
// in-flight state. State mutation is under m.mu; the disk write happens outside the lock.
func (m *Manager) observe(id string, lv *live, ev agentkit.Event) {
	m.mu.Lock()
	switch e := ev.(type) {
	case agentkit.TurnStart:
		if e.Frame == 0 {
			lv.running = true
			lv.events = nil
			lv.forest = newForest()
		}
	case agentkit.ToolStart:
		if lv.forest != nil {
			lv.forest.start(e.ID, e.Frame, e.Tool, e.Args)
		}
	case agentkit.ToolEnd:
		if lv.forest != nil {
			lv.forest.end(e.ID, e.Result, errText(e.Err), e.Duration.Milliseconds())
		}
	case agentkit.TurnEnd:
		if e.Frame == 0 {
			var nodes []ToolNode
			if lv.forest != nil {
				nodes = lv.forest.snapshot()
			}
			lv.running = false
			lv.idleSince = time.Now()
			lv.input = ""
			lv.events = nil
			lv.forest = nil
			m.mu.Unlock()
			if err := m.store.AppendTools(id, nodes); err != nil { // outside the lock (disk I/O)
				m.log.Warn("tool forest persist failed", "chat", id, "err", err)
			}
			// The single server-side handle for an interactive turn's stop reason: agentkit returns it
			// (doesn't log) and the client gets it via the TurnEnd event, but the operator sees nothing
			// unless we log it here. Agent runs take a different path (the scheduler logs those).
			if e.Err != nil {
				m.log.Warn("turn ended with error", "chat", id, "err", e.Err)
			}
			return
		}
	}
	// Buffer the running turn's events for a mid-turn reopen — the SAME stream the live broadcast
	// delivers, so a reopen and a live turn render by one client-side fold. Skipped once TurnEnd(frame
	// 0) has cleared the turn (it returns above).
	if lv.running {
		lv.events = append(lv.events, ev)
	}
	m.mu.Unlock()
}

// Tools returns a chat's persisted per-turn tool forest, for rebuilding the nested forest on snapshot.
func (m *Manager) Tools(id string) ([][]ToolNode, error) { return m.store.LoadTools(id) }

// InflightTurn is the running turn handed to a client reopening mid-turn: the user's input (recorded
// on submit, not yet persisted, not a stream event) plus the raw events streamed so far. The client
// replays the events through the SAME fold as the live stream, so a reopen and a live turn render by
// one path — no server-side render model to keep in sync. Running is false (zero value) when no turn
// is active; then the transcript alone is current.
type InflightTurn struct {
	Running bool
	Input   string
	Events  []agentkit.Event
}

// Inflight returns the running turn's state for id (zero value if the chat is not live or idle). The
// events slice is copied under the lock — the live turn keeps appending to lv.events after we return.
func (m *Manager) Inflight(id string) InflightTurn {
	m.mu.Lock()
	defer m.mu.Unlock()
	lv := m.active[id]
	if lv == nil || (!lv.running && lv.input == "") {
		return InflightTurn{}
	}
	events := make([]agentkit.Event, len(lv.events))
	copy(events, lv.events)
	return InflightTurn{
		Running: true, // input recorded or turn started — a turn is in progress
		Input:   lv.input,
		Events:  events,
	}
}

// errText renders an error as a wire string ("" for nil) — the store keeps a captured tool's error as
// text, mirroring the live ToolEnd wire form.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// forest accumulates one turn's tool calls from the live event stream, keyed by call id, in first-seen
// (start) order so parents precede children — the structure persisted as the turn's forest group at
// close. It is mutated only under Manager.mu (the pump writes it via observe).
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
	var reaped []string
	m.mu.Lock()
	for id, lv := range m.active {
		if !lv.running && now.Sub(lv.idleSince) > idleTTL {
			dead = append(dead, lv.sess)
			reaped = append(reaped, id)
			delete(m.active, id) // remove FIRST so a concurrent Open spins a fresh one
		}
	}
	m.mu.Unlock()
	for i, s := range dead {
		s.Close() // closes the stream → its pump exits
		m.log.Debug("session reaped", "chat", reaped[i])
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
	m.log.Info("chat deleted", "chat", id)
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
	m.log.Info("closing all sessions", "count", len(sessions))
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
