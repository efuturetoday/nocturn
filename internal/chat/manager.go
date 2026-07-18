package chat

import (
	"context"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/wakecap"
)

// Manager is the runtime side of multi-chat: it turns the persistent Store into LIVE chats —
// one Chat per open conversation, seeded from its saved history and saving back on every
// turn. Several chats in a workspace run concurrently and keep running across client
// reconnects, so a background chat's wake/turn is not lost when the app disconnects.
//
// It owns no authority and knows no agent concept: the workspace supplies charter
// factories (Deps.Root), the Manager only wraps a constructed Chat in a loop + a
// persistence pump. Every chat's effects still pass the workspace's Guard (broker +
// HITL). The pump doubles as a permanent tap, so a chat always records an approval as
// pending — a reconnecting app sees it in the snapshot and answers it in-app (the
// signed token never leaves the daemon).

// Deps is what the Manager needs to spin a chat — the workspace's shared parts plus
// the chat Store. Root mints the charter a fresh/reopened chat is constructed under; it
// is a factory (not a value) so a chat opened after a persona change seeds the current
// one. Agent resolves a named agent's charter for in-chat spawns (SubmitAgent).
type Deps struct {
	Engine *brain.Brain
	Guard  *gateway.Guard
	Store  *Store
	Root   func() Charter
	Agent  func(name string) (Charter, error)
	// OnActivity, if set, is called from a chat's pump with the chat id and a kind
	// ("turnEnd" | "approvalPending") so a host can badge background chats. Optional (nil =
	// no signal); it must not block — the pump calls it inline.
	OnActivity func(chatID, kind string)
}

// Manager owns a workspace's live chats. ctx is the RUNTIME lifetime (the daemon/session):
// chats start on it so they keep running independently of any one client connection —
// it is deliberately not a per-request context.
type Manager struct {
	ctx  context.Context
	deps Deps

	mu   sync.Mutex
	live map[string]*Chat
}

// NewManager builds a manager over deps whose chats live for as long as ctx.
func NewManager(ctx context.Context, deps Deps) *Manager {
	return &Manager{ctx: ctx, deps: deps, live: map[string]*Chat{}}
}

// List returns every chat's metadata (most recent first).
func (m *Manager) List() []Meta { return m.deps.Store.List() }

// AnyRunning reports whether any live chat is mid-turn — the workspace picker's "busy" flag.
func (m *Manager) AnyRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.live {
		if c.Snapshot().Running {
			return true
		}
	}
	return false
}

// New mints a fresh chat IN MEMORY (no file yet) and spins it immediately, so a
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
	m.spinLocked(meta, nil)
	return meta, nil
}

// Rename updates a chat's name (live and persisted).
func (m *Manager) Rename(id, name string) error {
	m.mu.Lock()
	if c := m.live[id]; c != nil {
		c.rename(name)
	}
	m.mu.Unlock()
	return m.deps.Store.Rename(id, name)
}

// Delete stops a chat (if live) and removes it from the store.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	if c := m.live[id]; c != nil {
		delete(m.live, id) // remove FIRST so the pump sees it gone and skips a stale save
		c.Close()
	}
	m.mu.Unlock()
	return m.deps.Store.Delete(id)
}

// Open returns the live chat: the same one if it is already live, otherwise it
// loads the saved history and spins a fresh chat seeded with it. ok is false for an
// unknown/invalid id (a memory-minted chat is found live, not in the store).
func (m *Manager) Open(id string) (*Chat, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.live[id]; c != nil {
		return c, true
	}
	msgs, meta, ok := m.deps.Store.Load(id)
	if !ok {
		return nil, false
	}
	return m.spinLocked(meta, msgs), true
}

// spinLocked constructs a live chat under the workspace's root charter — seeded with
// history, its loop on the manager's runtime ctx, plus a persistence pump — registers
// it, and returns it. The caller holds m.mu. The chat's lifetime is the manager ctx,
// so it keeps running across client disconnects.
func (m *Manager) spinLocked(meta Meta, msgs []brain.Message) *Chat {
	id := meta.ID
	c := New(m.deps.Engine, m.deps.Guard, meta, m.deps.Root(),
		WithHistory(msgs),
		WithAgents(m.deps.Agent),
		// Bind wake to THIS chat: a turn's ctx carries a resume that Delivers back to this
		// id, so the workspace-shared wake tool resumes the chat that invoked it (not an
		// ambient one).
		WithDecorator(func(ctx context.Context) context.Context {
			return wakecap.WithResume(ctx, func(note string) { m.Deliver(id, SourceWake, note) })
		}))
	c.Start(m.ctx)

	// Persistence pump: a permanent TAP (not a Subscribe) that saves after every turn. A tap
	// fans out the same events but does NOT count as a watching client, so an unwatched
	// background chat stays "no live client" — its approval records as pending (the sink is
	// always stamped) AND goes out-of-band to the phone. Close closes the tap channel, so
	// the pump exits with the chat.
	sub, _ := c.Tap()
	go m.pump(c, sub)
	m.live[id] = c
	return c
}

// pump saves a chat's conversation after every turn and badges background activity. It runs
// until the tap channel closes (on Close/Delete/CloseAll). Lazy-persist: a turn that
// produced no messages is never written, so an untouched chat leaves no file.
func (m *Manager) pump(c *Chat, sub <-chan Event) {
	for e := range sub {
		switch e.(type) {
		case TurnEndEvent:
			meta := c.Meta() // name may have changed via Rename; read it fresh
			m.mu.Lock()
			_, stillLive := m.live[meta.ID] // a concurrent Delete dropped it → skip the stale save
			m.mu.Unlock()
			if !stillLive {
				continue
			}
			msgs := c.Snapshot().Messages
			if len(msgs) == 0 {
				continue // lazy-persist: never write an empty chat to disk
			}
			_ = m.deps.Store.Save(meta.ID, meta.Name, meta.Origin, msgs)
			m.signal(meta.ID, "turnEnd")
		case ApprovalEvent:
			// A background chat is waiting on an approval — actionable, not just a badge.
			m.signal(c.Meta().ID, "approvalPending")
		}
	}
}

// Deliver routes an input into a chat's turn loop, spinning the chat from disk if it isn't
// already live. It is how a trigger with no client — a fired wake, later a cron agent —
// resumes a specific chat. A deleted or unknown chat is a no-op: Open returns false, so a
// stale wake never resurrects a chat the user removed.
func (m *Manager) Deliver(id string, source Source, input string) {
	if c, ok := m.Open(id); ok {
		c.Submit(source, input)
	}
}

// MarkSkill records a /name-activated skill as loaded on a live chat, so a later
// model skill.load dedups. A no-op for a chat that isn't live.
func (m *Manager) MarkSkill(id, name string) {
	m.mu.Lock()
	c := m.live[id]
	m.mu.Unlock()
	if c != nil {
		c.MarkSkill(name)
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
	// Detach the live set under the lock, then Save (file I/O) + Close OUTSIDE it — keep the
	// critical section short and never hold m.mu across a disk write.
	m.mu.Lock()
	live := m.live
	m.live = map[string]*Chat{}
	m.mu.Unlock()

	for _, c := range live {
		snap := c.Snapshot()
		if len(snap.Messages) > 0 { // lazy: don't persist an empty chat
			meta := c.Meta()
			_ = m.deps.Store.Save(meta.ID, meta.Name, meta.Origin, snap.Messages)
		}
		c.Close()
	}
}
