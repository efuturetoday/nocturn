package chat

import (
	"context"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/session"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Manager is the runtime side of multi-chat: it turns the persistent Store into LIVE chats —
// one session.Runner per open chat, seeded from its saved history and saving back on every
// turn. Several chats in a workspace run concurrently and keep running across client
// reconnects, so a background chat's wake/turn is not lost when the app disconnects.
//
// It owns no authority: every chat's effects still pass the workspace's Guard (broker +
// HITL). The persistence pump doubles as a permanent subscriber, so a chat's runner always
// records an approval as pending — a reconnecting app sees it in the snapshot and answers it
// in-app (the signed token never leaves the daemon).

// Deps is what the Manager needs to spin a chat's runner — the workspace's shared parts plus
// the chat Store. Persona is a getter (not a value) so a chat opened after a persona change
// seeds the current one. AgentRun wires SubmitAgent to the workspace's child-agent runner.
type Deps struct {
	Brain    *brain.Brain
	Tools    *tool.Registry
	Guard    *gateway.Guard
	Grants   capability.GrantStore
	Persona  func() string
	Store    *Store
	AgentRun func(ctx context.Context, name, task string) (string, error)
	// OnActivity, if set, is called from a chat's pump with the chat id and a kind
	// ("turnEnd" | "approvalPending") so a host can badge background chats. Optional (nil =
	// no signal); it must not block — the pump calls it inline.
	OnActivity func(chatID, kind string)
}

// Manager owns a workspace's live chats. ctx is the RUNTIME lifetime (the daemon/session):
// runners start on it so a chat keeps running independently of any one client connection —
// it is deliberately not a per-request context.
type Manager struct {
	ctx  context.Context
	deps Deps

	mu   sync.Mutex
	live map[string]*liveChat
}

type liveChat struct {
	runner  *session.Runner
	session *session.Session
	unsub   func()
	id      string
	name    string
	origin  Origin
}

// NewManager builds a manager over deps whose runners live for as long as ctx.
func NewManager(ctx context.Context, deps Deps) *Manager {
	return &Manager{ctx: ctx, deps: deps, live: map[string]*liveChat{}}
}

// List returns every chat's metadata (most recent first).
func (m *Manager) List() []Meta { return m.deps.Store.List() }

// New mints a fresh chat IN MEMORY (no file yet) and spins its runner immediately, so a
// trigger (or the app) can drive it before it is ever saved. The first turn's save creates
// the file — a chat that never takes a turn leaves nothing behind (lazy-persist). name and
// origin default when blank.
func (m *Manager) New(name string, origin Origin) (Meta, error) {
	if name == "" {
		name = "New chat"
	}
	if origin == "" {
		origin = OriginUser
	}
	now := time.Now()
	meta := Meta{ID: m.deps.Store.NewID(), Name: name, Origin: origin, Created: now, Updated: now}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spinLocked(meta.ID, meta.Name, meta.Origin, nil)
	return meta, nil
}

// Rename updates a chat's name (live and persisted).
func (m *Manager) Rename(id, name string) error {
	m.mu.Lock()
	if lc := m.live[id]; lc != nil {
		lc.name = name
	}
	m.mu.Unlock()
	return m.deps.Store.Rename(id, name)
}

// Delete stops a chat's live runner (if any) and removes it from the store.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	if lc := m.live[id]; lc != nil {
		delete(m.live, id) // remove FIRST so the pump sees it gone and skips a stale save
		lc.stop()
	}
	m.mu.Unlock()
	return m.deps.Store.Delete(id)
}

// Open returns the chat's live runner: the same one if it is already live, otherwise it
// loads the saved history and spins a fresh runner seeded with it. ok is false for an
// unknown/invalid id (a memory-minted chat is found via its live runner, not the store).
func (m *Manager) Open(id string) (*session.Runner, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lc := m.live[id]; lc != nil {
		return lc.runner, true
	}
	msgs, meta, ok := m.deps.Store.Load(id)
	if !ok {
		return nil, false
	}
	return m.spinLocked(meta.ID, meta.Name, meta.Origin, msgs), true
}

// spinLocked creates a chat's live runner — a session seeded with history + the current
// persona, the runner on the manager's runtime ctx, and a persistence pump — registers it,
// and returns it. The caller holds m.mu. The runner's lifetime is the manager ctx, so the
// chat keeps running across client disconnects.
func (m *Manager) spinLocked(id, name string, origin Origin, msgs []brain.Message) *session.Runner {
	sess := session.New(m.deps.Brain, m.deps.Tools, m.deps.Guard, m.deps.Grants,
		session.WithPersona(m.deps.Persona()), session.WithHistory(msgs))
	runner := session.NewRunner(sess, session.WithAgentRunner(m.deps.AgentRun))
	runner.Start(m.ctx)

	lc := &liveChat{runner: runner, session: sess, id: id, name: name, origin: origin}
	// Persistence pump: a permanent TAP (not a Subscribe) that saves after every turn. A tap
	// fans out the same events but does NOT count as a watching client, so an unwatched
	// background chat stays "no live client" — its approval records as pending (the sink is
	// always stamped) AND goes out-of-band to the phone.
	sub, unsub := runner.Tap()
	lc.unsub = unsub
	go m.pump(id, runner, sub)
	m.live[id] = lc
	return runner
}

// pump saves a chat's conversation after every turn and badges background activity. It runs
// until the tap channel closes (on stop/Delete/CloseAll). Lazy-persist: a turn that
// produced no messages is never written, so an untouched chat leaves no file.
func (m *Manager) pump(id string, runner *session.Runner, sub <-chan session.Event) {
	for e := range sub {
		switch e.(type) {
		case session.TurnEndEvent:
			// Read name/origin WHILE holding the lock — Rename writes lc.name under m.mu. A
			// concurrent Delete drops it from the map → stillLive false → skip the stale save.
			m.mu.Lock()
			cur, stillLive := m.live[id]
			var name string
			var origin Origin
			if stillLive {
				name, origin = cur.name, cur.origin
			}
			m.mu.Unlock()
			if !stillLive {
				continue
			}
			msgs := runner.Snapshot().Messages
			if len(msgs) == 0 {
				continue // lazy-persist: never write an empty chat to disk
			}
			_ = m.deps.Store.Save(id, name, origin, msgs)
			m.signal(id, "turnEnd")
		case session.ApprovalEvent:
			// A background chat is waiting on an approval — actionable, not just a badge.
			m.signal(id, "approvalPending")
		}
	}
}

// signal fires the optional activity hook (badge a background chat). Kinds are the two the
// host contract expects; see Deps.OnActivity.
func (m *Manager) signal(chatID, kind string) {
	if m.deps.OnActivity != nil {
		m.deps.OnActivity(chatID, kind)
	}
}

// CloseAll saves and stops every live chat (on shutdown).
func (m *Manager) CloseAll() {
	// Detach the live set under the lock, then Save (file I/O) + stop OUTSIDE it — keep the
	// critical section short and never hold m.mu across a disk write.
	m.mu.Lock()
	live := m.live
	m.live = map[string]*liveChat{}
	m.mu.Unlock()

	for _, lc := range live {
		if msgs := lc.runner.Snapshot().Messages; len(msgs) > 0 {
			_ = m.deps.Store.Save(lc.id, lc.name, lc.origin, msgs) // lazy: don't persist an empty chat
		}
		lc.stop()
	}
}

// stop ends the persistence pump (unsubscribe closes its channel) and closes the session
// (revoking its scope). The runner loop itself ends when the manager's ctx is cancelled.
func (lc *liveChat) stop() {
	if lc.unsub != nil {
		lc.unsub()
	}
	lc.session.Close()
}
