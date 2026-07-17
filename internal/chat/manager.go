package chat

import (
	"context"
	"sync"

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
}

// NewManager builds a manager over deps whose runners live for as long as ctx.
func NewManager(ctx context.Context, deps Deps) *Manager {
	return &Manager{ctx: ctx, deps: deps, live: map[string]*liveChat{}}
}

// List returns every chat's metadata (most recent first).
func (m *Manager) List() []Meta { return m.deps.Store.List() }

// New creates a new empty chat and returns its metadata; its runner spins on first Open.
func (m *Manager) New(name string) (Meta, error) { return m.deps.Store.Create(name) }

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

// Open returns the chat's live runner, lazily creating it: it loads the saved history, opens
// a session seeded with it (and the current persona), starts the runner on the manager's
// runtime ctx, and attaches a persistence pump that saves after every turn. A subsequent
// Open returns the same running runner. ok is false for an unknown/invalid id.
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
	sess := session.New(m.deps.Brain, m.deps.Tools, m.deps.Guard, m.deps.Grants,
		session.WithPersona(m.deps.Persona()), session.WithHistory(msgs))
	runner := session.NewRunner(sess, session.WithAgentRunner(m.deps.AgentRun))
	runner.Start(m.ctx)

	lc := &liveChat{runner: runner, session: sess, id: id, name: meta.Name}
	// Persistence pump: a permanent subscriber that saves the conversation after every turn.
	// Being always-subscribed also keeps the runner's approval sink stamped, so an approval
	// is recorded as pending even with no live client — the app answers it on reconnect.
	sub, unsub := runner.Subscribe()
	lc.unsub = unsub
	go func() {
		for e := range sub {
			if _, ok := e.(session.TurnEndEvent); ok {
				// Copy the name WHILE holding the lock — Rename writes lc.name under m.mu,
				// so reading it unlocked would race. A concurrent Delete drops it from the
				// map → stillLive false → skip the stale save.
				m.mu.Lock()
				cur, stillLive := m.live[id]
				var name string
				if stillLive {
					name = cur.name
				}
				m.mu.Unlock()
				if stillLive {
					_ = m.deps.Store.Save(id, name, runner.Snapshot().Messages)
				}
			}
		}
	}()

	m.live[id] = lc
	return runner, true
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
		_ = m.deps.Store.Save(lc.id, lc.name, lc.runner.Snapshot().Messages)
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
