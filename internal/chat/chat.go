// Package chat is the conversation unit and its runtime. A Chat is a live
// conversation constructed under a Charter — (tools, system prompt, authority,
// budget) — handed in from outside: history + revocable permission scope + loaded
// skills, driven by a serialized turn loop (commands in via Submit/Cancel/Reset/
// Resolve, events out via Subscribe). The Store persists chats and the Manager
// keeps N of them live per workspace. A user chat and an agent run are the same
// machinery; who mints the Charter is the only difference.
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

	// runAgent wires a SubmitAgent command to the workspace's child-agent runner.
	// Transitional: step 4 of the redesign absorbs agent runs into chat.Once.
	runAgent func(ctx context.Context, name, task string) (string, error)
	decorate func(context.Context) context.Context // per-chat ctx decoration (the wake-binding seam)
	history  []brain.Message                       // WithHistory seed; consumed at New, then dropped

	parent context.Context // the runtime lifetime the loop runs on (set by Start)
	cmds   chan command
	done   chan turnResult

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
}

// Option configures a Chat built with New.
type Option func(*Chat)

// WithHistory seeds a REOPENED chat with its saved turns (after the system prompt).
// Used once, at New; Reset deliberately starts empty (a cleared chat), so history is
// not re-seeded there.
func WithHistory(msgs []brain.Message) Option {
	return func(c *Chat) { c.history = msgs }
}

// WithDecorator stamps decoration onto every turn's ctx — the seam a caller uses to
// bind chat-scoped identity a tool reads at call time (e.g. "wake resumes THIS
// chat", so the workspace-shared wake tool resolves its target from ctx instead of
// a static wire).
func WithDecorator(fn func(context.Context) context.Context) Option {
	return func(c *Chat) { c.decorate = fn }
}

// WithAgentRunner wires how a SubmitAgent command runs a named child agent to a
// final answer (in production, the workspace's agent runner). Without it,
// SubmitAgent fails the turn with an error — a chat-only client never submits one.
func WithAgentRunner(fn func(ctx context.Context, name, task string) (string, error)) Option {
	return func(c *Chat) { c.runAgent = fn }
}

// New builds a chat on engine under charter: it mints the chat's Scope from the
// charter's Authority (a fresh epoch + grant set on the guard's registry) and a
// fresh conversation over the charter's tools, seeded with the charter's system
// prompt and any WithHistory turns. Call Start to spin the turn loop.
func New(engine *brain.Brain, guard *gateway.Guard, meta Meta, ch Charter, opts ...Option) *Chat {
	c := &Chat{
		meta:    meta,
		charter: ch,
		engine:  engine,
		guard:   guard,
		scope:   guard.NewScope(ch.Authority),
		skills:  skill.NewActive(),
		cmds:    make(chan command, 8),
		done:    make(chan turnResult, 1),
		subs:    map[int]chan Event{},
		taps:    map[int]chan Event{},
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

// rename updates the chat's display name (the Manager persists it separately).
func (c *Chat) rename(name string) {
	c.mu.Lock()
	c.meta.Name = name
	c.mu.Unlock()
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

// Close ends the chat: it revokes the scope (its session grants die at once) and
// closes every subscriber and tap channel so their readers exit. The loop itself
// ends when the runtime ctx is cancelled. Idempotent; a later unsubscribe of an
// already-closed channel is a no-op.
func (c *Chat) Close() {
	c.mu.Lock()
	subs, taps := c.subs, c.taps
	c.subs, c.taps = map[int]chan Event{}, map[int]chan Event{}
	scope := c.scope
	c.mu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
	for _, ch := range taps {
		close(ch)
	}
	scope.Revoke()
}
