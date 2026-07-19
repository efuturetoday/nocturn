// Package chat is the conversation unit and its runtime. A Chat is a live
// conversation constructed under a Charter — (tools, system prompt, authority,
// budget) — handed in from outside: history + revocable permission scope + loaded
// skills, driven by a serialized turn loop (commands in via Submit/Cancel/Reset/
// Resolve, events out via Subscribe). The Store persists chats and the Manager
// keeps N of them live per workspace. A user chat and an agent run are the same
// machinery; who mints the Charter is the only difference — and lifetime is a
// choice, not a type: an attended in-chat spawn runs as a throwaway Once inside
// the parent turn, a cron firing becomes a fresh one-shot chat (Manager.FireAgent)
// whose persisted record is the run's audit trail.
//
// One Chat bundles everything that resets and closes together:
//
//   - a brain.Conversation (the message history),
//   - a gateway.Scope minted from the Charter's Authority (the revocable epoch +
//     standing grants; the Guard owns the epoch mechanism, so the chat never
//     touches the EpochRegistry directly),
//   - the set of skills loaded into this conversation (for skill.load dedup),
//   - the loop state (queue, subscribers, pending approval — see loop.go).
//
// Every turn runs under the chat's Scope (threaded through ctx via Bind in turn —
// the single chat-level ctx writer): "Allow this session" grants bind to the
// scope's epoch, "Allow always" grants persist. Reset revokes the scope and opens
// a fresh one — the persistent "always" grants (in the durable store) survive.
//
// The loop carries no transport and no approval-mechanism types; approvals
// surface as events enacted through an opaque callback (see ApprovalSink) — a
// TUI, a REST/SSE server, or a mobile app all drive a Chat the same way.
package chat

import (
	"context"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/deadline"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/skill"
)

// Chat is one live conversation: its identity (Meta), its construction spec
// (Charter), and its runtime state. Commands and events flow through the loop in
// loop.go; the state here is what a turn executes against.
type Chat struct {
	meta    Meta
	charter Charter
	engine  *brain.Brain
	guard   *gateway.Guard

	// agents resolves a named agent's charter for an in-chat spawn (SubmitAgent);
	// the loop runs the spawn as a one-shot Once inside the parent turn.
	agents   func(name string) (Charter, error)
	decorate func(context.Context) context.Context // per-chat ctx decoration (the wake-binding seam)
	history  []brain.Message                       // WithHistory seed; consumed at New, then dropped

	parent context.Context // the runtime lifetime the loop runs on (set by Start)
	cmds   chan command
	done   chan turnResult
	quit   chan struct{} // closed by Close: the loop exits, pending senders abort

	// mu guards BOTH field groups below as one invariant — Snapshot reads the loop
	// state and the conv pointer under the same lock, Reset swaps conv/scope/skills
	// while dropping the queue. That shared lock is why the loop state is declared
	// here next to its mutex (Google style: keep a mutex adjacent to what it guards)
	// rather than embedded from loop.go, which only holds the loop's BEHAVIOR.
	mu sync.Mutex
	// conv/scope/skills are swapped as one unit by resetState (under mu, from the
	// loop); a turn captures them at its start and keeps its own references.
	conv   *brain.Conversation
	scope  *gateway.Scope
	skills *skill.Active
	// The turn loop's state (driven by loop.go).
	subs       map[int]chan Event // real clients watching the stream — count decides Ask-time attendance
	taps       map[int]chan Event // passive observers (the persistence pump) — never count as attendance
	nextID     int                // mints unique ids for both subs and taps
	running    bool
	queue      []queuedInput
	cancelTurn context.CancelFunc
	approval   *pendingApproval
	nextAppr   int
	closed     bool // Close ran: quit is closed, subs/taps drained — makes Close idempotent

	// forest accumulates every completed tool invocation (in stream order) so a reload
	// rebuilds the full call tree — sub-calls and their errors included — that the message
	// history alone cannot. forestIx maps an invocation ID to its slot for the End fill-in.
	forest   []ToolFrame
	forestIx map[uint64]int
}

// Option configures a Chat built with New.
type Option func(*Chat)

// WithHistory seeds a REOPENED chat with its saved turns (after the system prompt).
// Used once, at New; Reset deliberately starts empty (a cleared chat), so history is
// not re-seeded there.
func WithHistory(msgs []brain.Message) Option {
	return func(c *Chat) { c.history = msgs }
}

// WithForest seeds a REOPENED chat with its saved tool forest, so the first snapshot after
// reload carries the same call tree the chat streamed live. Used once, at New.
func WithForest(frames []ToolFrame) Option {
	return func(c *Chat) { c.forest = frames }
}

// WithDecorator stamps decoration onto every turn's ctx — the seam a caller uses to
// bind chat-scoped identity a tool reads at call time (e.g. "wake resumes THIS
// chat", so the workspace-shared wake tool resolves its target from ctx instead of
// a static wire).
func WithDecorator(fn func(context.Context) context.Context) Option {
	return func(c *Chat) { c.decorate = fn }
}

// WithAgents wires the charter resolver for in-chat agent spawns: a SubmitAgent
// turn resolves the named agent's charter (in production, the workspace's
// AgentCharter) and runs it as a one-shot Once inside the parent turn. Without
// it, SubmitAgent fails the turn with an error — a chat-only client never
// submits one.
func WithAgents(resolve func(name string) (Charter, error)) Option {
	return func(c *Chat) { c.agents = resolve }
}

// New builds a chat on engine under charter: it mints the chat's Scope from the
// charter's Authority (a fresh epoch + grant set on the guard's registry) and a
// fresh conversation over the charter's tools, seeded with the charter's system
// prompt and any WithHistory turns. Call Start to spin the turn loop.
func New(engine *brain.Brain, guard *gateway.Guard, meta Meta, ch Charter, opts ...Option) *Chat {
	c := &Chat{
		meta:     meta,
		charter:  ch,
		engine:   engine,
		guard:    guard,
		scope:    guard.NewScope(ch.Authority),
		skills:   skill.NewActive(),
		cmds:     make(chan command, 8),
		done:     make(chan turnResult, 1),
		quit:     make(chan struct{}),
		subs:     map[int]chan Event{},
		taps:     map[int]chan Event{},
		forestIx: map[uint64]int{},
	}
	for _, o := range opts {
		o(c)
	}
	c.conv = engine.NewConversation(ch.Tools, brain.WithSystem(ch.System), brain.WithHistory(c.history))
	c.history = nil
	return c
}

// Meta returns the chat's identity (id, name, origin, timestamps).
func (c *Chat) Meta() Meta {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.meta
}

// The chat's identity Meta (name + the store-owned timestamps Updated/Read + Turns) is OWNED
// here, in the domain — the Store is a pure serializer that persists whatever Meta it is handed
// and invents no values. These three methods advance the live Meta and return the fresh copy for
// the Manager to hand to Store.Save; keeping the live Meta authoritative is what lets List show an
// OPEN chat's new turns and read cursor (List trusts the live Meta over the disk copy).

// touch advances Updated to now and recounts Turns to reflect a just-completed turn.
func (c *Chat) touch(msgs []brain.Message) Meta {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.meta.Updated = time.Now()
	c.meta.Turns = countUserTurns(msgs)
	return c.meta
}

// markRead advances the read cursor to the current Updated ("read up to the latest turn").
func (c *Chat) markRead() Meta {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.meta.Read = c.meta.Updated
	return c.meta
}

// rename sets the display name and bumps Updated (a rename reorders the list).
func (c *Chat) rename(name string) Meta {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.meta.Name = name
	c.meta.Updated = time.Now()
	return c.meta
}

// countUserTurns is the "N messages" hint: how many user messages a transcript holds.
func countUserTurns(msgs []brain.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// turnState is the conversation/scope/skills one turn executes against. It is
// captured at begin — ON the loop goroutine, serialized with onReset — so a Reset
// that lands between TurnStart and the turn goroutine's first step cancels THIS
// turn's (old) state instead of letting the doomed turn leak onto the fresh
// conversation.
type turnState struct {
	conv   *brain.Conversation
	scope  *gateway.Scope
	skills *skill.Active
}

// turn is THE turn ceremony — the one place a chat-level permission ctx is built.
// It binds the scope (grants + policy + cage + autonomy + label, all from the
// Charter's Authority, in one Scope.Bind), stamps the active-skills set, applies
// the charter's wall-clock budget, and drives the conversation. The orchestration
// ctx (cancel, activity sink, approval sink, decorator) is built by the loop's
// begin — see loop.go.
func (c *Chat) turn(ctx context.Context, st turnState, input string) (string, error) {
	ctx = st.scope.Bind(ctx)
	ctx = skill.WithActive(ctx, st.skills)
	if c.charter.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = deadline.WithBudget(ctx, c.charter.Budget)
		defer cancel()
	}
	return st.conv.Send(ctx, input)
}

// resetState starts the chat over: it revokes the old scope (revoking every "Allow
// this session" grant bound to its epoch) and opens a fresh scope, conversation and
// skill set from the retained charter. Persistent "always" grants survive. Called
// from the loop's onReset — the cancelled turn keeps its own old references and
// unwinds against the revoked scope (fail-closed).
func (c *Chat) resetState() {
	c.mu.Lock()
	old := c.scope
	c.scope = c.guard.NewScope(c.charter.Authority)
	c.conv = c.engine.NewConversation(c.charter.Tools, brain.WithSystem(c.charter.System))
	c.skills = skill.NewActive()
	c.mu.Unlock()
	old.Revoke()
}

// MarkSkill records a skill as already loaded in this conversation — used by the
// explicit /name path, which injects the body itself, so a later model-issued
// skill.load for the same skill is deduplicated instead of re-injecting it.
func (c *Chat) MarkSkill(name string) {
	c.mu.Lock()
	skills := c.skills
	c.mu.Unlock()
	skills.Mark(name)
}

// Close ends the chat: it revokes the scope (its session grants die at once),
// stops the loop (via quit — so a reaped one-shot's goroutine does not linger
// until process shutdown), and closes every subscriber and tap channel so their
// readers exit. An in-flight turn keeps running on its own ctx and fails closed
// against the revoked scope. Idempotent; a later unsubscribe of an
// already-closed channel is a no-op.
func (c *Chat) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	subs, taps := c.subs, c.taps
	c.subs, c.taps = map[int]chan Event{}, map[int]chan Event{}
	scope := c.scope
	c.mu.Unlock()
	close(c.quit)
	for _, ch := range subs {
		close(ch)
	}
	for _, ch := range taps {
		close(ch)
	}
	scope.Revoke()
}
