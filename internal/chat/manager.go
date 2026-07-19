package chat

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/tool"
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
	// OnActivity, if set, is called from a chat's pump with the chat id, a kind
	// ("turnEnd" | "approvalPending"), and the chat's origin ("user" | "agent") so a host can
	// badge background chats AND decide whether to wake a backgrounded user (a user chat's
	// turnEnd), not an autonomous agent run. Optional (nil = no signal); it must not block —
	// the pump calls it inline.
	OnActivity func(chatID, kind, origin string)
	// OnChange, if set, fires on any change to THIS workspace's chat LIST (a chat created,
	// deleted, renamed, or completing a turn) so a host can push the full list to clients —
	// coarse list-sync, distinct from the per-chat OnActivity badge. Optional; must not block.
	OnChange func()
}

// Manager owns a workspace's live chats. ctx is the RUNTIME lifetime (the daemon/session):
// chats start on it so they keep running independently of any one client connection —
// it is deliberately not a per-request context.
type Manager struct {
	ctx  context.Context
	deps Deps

	pumps sync.WaitGroup // one per spun chat; CloseAll waits so no save outlives shutdown

	mu   sync.Mutex
	live map[string]*Chat
}

// NewManager builds a manager over deps whose chats live for as long as ctx.
func NewManager(ctx context.Context, deps Deps) *Manager {
	return &Manager{ctx: ctx, deps: deps, live: map[string]*Chat{}}
}

// List returns every chat's metadata (most recent first).
func (m *Manager) List() []Meta {
	saved := m.deps.Store.List() // persisted chats (disk)
	// Include LIVE chats too — a freshly-created chat lives in memory with no file yet
	// (lazy-persist), so a store-only listing would hide it until its first turn. Collect the
	// live chats under the lock, then read each Meta outside it (Meta takes the chat's own
	// lock — never nest the two).
	m.mu.Lock()
	liveChats := make([]*Chat, 0, len(m.live))
	for _, c := range m.live {
		liveChats = append(liveChats, c)
	}
	m.mu.Unlock()

	out := make([]Meta, 0, len(liveChats)+len(saved))
	seen := make(map[string]bool, len(liveChats))
	for _, c := range liveChats {
		meta := c.Meta()
		out = append(out, meta)
		seen[meta.ID] = true
	}
	for _, meta := range saved {
		if !seen[meta.ID] { // live wins over its (possibly staler) saved copy
			out = append(out, meta)
		}
	}
	slices.SortFunc(out, func(a, b Meta) int { return b.Updated.Compare(a.Updated) })
	return out
}

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
	m.spinLocked(meta, m.deps.Root(), nil, nil)
	m.mu.Unlock()
	m.changed() // a new chat appeared in the list
	return meta, nil
}

// Rename updates a chat's name (live and persisted).
func (m *Manager) Rename(id, name string) error {
	m.mu.Lock()
	if c := m.live[id]; c != nil {
		c.rename(name)
	}
	m.mu.Unlock()
	err := m.deps.Store.Rename(id, name)
	m.changed()
	return err
}

// Delete stops a chat (if live) and removes it from the store.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	if c := m.live[id]; c != nil {
		delete(m.live, id) // remove FIRST so the pump sees it gone and skips a stale save
		c.Close()
	}
	m.mu.Unlock()
	err := m.deps.Store.Delete(id)
	m.changed()
	return err
}

// Open returns the live chat: the same one if it is already live, otherwise it
// loads the saved record and spins a fresh chat seeded with its history — UNIFORM
// over both kinds: a root/user chat reopens under the Root charter, an agent run
// (Meta.Agent set) under that agent's charter. Reopening is an ATTENDED act (a
// client asked for it), so the agent factory's normal-HITL charter applies. ok is
// false only for an unknown/invalid id. A run whose agent declaration no longer
// exists still opens — the transcript is the USER'S data — but under a
// zero-authority viewer charter (no tools, no grants): fail-closed applies to
// effects, never to reading one's own history.
func (m *Manager) Open(id string) (*Chat, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openLocked(id)
}

func (m *Manager) openLocked(id string) (*Chat, bool) {
	if c := m.live[id]; c != nil {
		return c, true
	}
	msgs, forest, meta, ok := m.deps.Store.Load(id)
	if !ok {
		return nil, false
	}
	ch := m.deps.Root()
	if meta.Agent != "" {
		var err error
		if ch, err = m.deps.Agent(meta.Agent); err != nil {
			// The declaration is gone (agent deleted) but the record is the user's
			// data: open it VIEWABLE under a zero-authority charter — an empty
			// toolset and a zero Authority (no grants, attended). The history
			// renders; a new turn is a bare model call with nothing to invoke, so
			// every effect path is structurally absent, not merely denied.
			ch = Charter{Tools: tool.NewRegistry()}
		}
	}
	return m.spinLocked(meta, ch, msgs, forest), true
}

// spinLocked constructs a live chat under charter — seeded with history, its loop on
// the manager's runtime ctx, plus a persistence pump — registers it, and returns it.
// The caller holds m.mu. The chat's lifetime is the manager ctx (or its reap, for an
// idle one-shot), so it keeps running across client disconnects.
func (m *Manager) spinLocked(meta Meta, ch Charter, msgs []brain.Message, forest []ToolFrame) *Chat {
	id := meta.ID
	c := New(m.deps.Engine, m.deps.Guard, meta, ch,
		WithHistory(msgs),
		WithForest(forest),
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
	m.pumps.Go(func() { m.pump(c, sub) })
	m.live[id] = c
	return c
}

// pump saves a chat's conversation after every turn, badges background activity, and
// REAPS an idle one-shot agent chat (its record on disk is the run's audit trail; the
// live instance has no reason to linger — a pending wake re-Delivers by id and Open
// revives it from the record). It runs until the tap channel closes (on
// reap/Delete/CloseAll). Lazy-persist: a turn that produced no messages is never
// written, so an untouched chat leaves no file.
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
			snap := c.Snapshot()
			if len(snap.Messages) > 0 { // lazy-persist: never write an empty chat to disk
				_ = m.deps.Store.Save(meta, snap.Messages, snap.Forest)
				m.signal(meta.ID, "turnEnd", string(meta.Origin))
				m.changed() // updated time / turn count reorder the list
			}
			// Reap an agent one-shot that has gone idle. The idle check runs under m.mu,
			// serialized with Deliver, so no delivery can land on the instance between
			// the check and its removal; running stays true across a queued handoff (see
			// onTurnDone), so idle really means "nothing left". Once removed from live,
			// the id revives from its saved record (Open) — the run's transcript is the
			// durable artifact, the live instance has no reason to linger.
			if meta.Agent != "" {
				m.mu.Lock()
				reap := m.live[meta.ID] == c && c.idle()
				if reap {
					delete(m.live, meta.ID)
				}
				m.mu.Unlock()
				if reap {
					c.Close() // closes this tap too — the pump exits with the chat
				}
			}
		case ApprovalEvent:
			// A background chat is waiting on an approval — actionable, not just a badge.
			meta := c.Meta()
			m.signal(meta.ID, "approvalPending", string(meta.Origin))
		}
	}
}

// Deliver routes an input into a chat's turn loop, spinning the chat from disk if it isn't
// already live. It is how a trigger with no client — a fired wake, a reminder — resumes a
// specific chat. A deleted or unknown chat is a no-op: openLocked returns false, so a
// stale wake never resurrects a chat the user removed. Open + Submit happen under m.mu,
// serialized with the reap, so a delivery never lands on an instance being reaped (a
// reaped id revives from its saved record instead).
func (m *Manager) Deliver(id string, source Source, input string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.openLocked(id); ok {
		c.Submit(source, input)
	}
}

// ErrAgentBusy reports a FireAgent skipped because a live chat of that agent is
// still working — at most one run per agent at a time (same-agent parallelism
// would race on its per-owner grant store and working state).
var ErrAgentBusy = errors.New("chat: agent still running — firing skipped")

// scheduledTask is the input a cron firing delivers to its one-shot agent chat.
const scheduledTask = "Run your scheduled task now."

// keepAgentRuns caps how many saved runs Prune retains per agent, so a frequent
// cron never floods the picker; the newest records are the useful audit trail.
const keepAgentRuns = 20

// FireAgent runs one scheduled firing of the named agent as a FRESH one-shot chat
// under charter (minted by the workspace with the agent's declared autonomy dial —
// no human attends a cron firing): a new id, Origin agent, empty history — no
// memory across firings; the persisted record is the run's audit trail, visible in
// the picker. It returns ErrAgentBusy (without minting anything) while a live chat
// of this agent is mid-work — at most one run per agent — and otherwise blocks
// until the firing's turn completes, returning the turn's error, so the scheduler
// can log a truthful done/failed and its own overlap window spans the real run.
// Old records beyond keepAgentRuns are pruned. ctx bounds the wait (scheduler
// shutdown), not the turn — the turn runs on the manager's runtime lifetime.
func (m *Manager) FireAgent(ctx context.Context, name string, ch Charter) error {
	m.mu.Lock()
	for _, c := range m.live {
		if c.Meta().Agent == name && !c.idle() {
			m.mu.Unlock()
			return ErrAgentBusy
		}
	}
	now := time.Now()
	meta := Meta{
		ID:      m.deps.Store.NewID(),
		Name:    name + " · " + now.Format("Jan 2 15:04"),
		Origin:  OriginAgent,
		Agent:   name,
		Created: now,
		Updated: now,
	}
	c := m.spinLocked(meta, ch, nil, nil)
	// Tap BEFORE submitting so the TurnEnd cannot be missed; a tap never counts as
	// attendance, so the run stays unattended (its Asks resolve per the charter's dial).
	sub, unsub := c.Tap()
	defer unsub()
	c.Submit(SourceSchedule, scheduledTask)
	m.mu.Unlock()
	m.changed() // a fresh agent run appeared in the list

	m.deps.Store.Prune(name, keepAgentRuns)

	for {
		select {
		case e, ok := <-sub:
			if !ok {
				return nil // the chat closed (reaped after its turn, or shutdown) — nothing left to wait for
			}
			if te, ok := e.(TurnEndEvent); ok {
				return te.Err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
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
func (m *Manager) signal(chatID, kind, origin string) {
	if m.deps.OnActivity != nil {
		m.deps.OnActivity(chatID, kind, origin)
	}
}

// changed fires the optional list-change hook (the chat list of this workspace changed, so a
// host can push the full list to clients). See Deps.OnChange.
func (m *Manager) changed() {
	if m.deps.OnChange != nil {
		m.deps.OnChange()
	}
}

// CloseAll saves and stops every live chat (on shutdown), then waits for every
// persistence pump to finish — no save may still be in flight when the process
// (or a test's temp dir) tears down.
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
			_ = m.deps.Store.Save(c.Meta(), snap.Messages, snap.Forest)
		}
		c.Close() // closes the pump's tap → the pump drains and exits
	}
	m.pumps.Wait()
}
