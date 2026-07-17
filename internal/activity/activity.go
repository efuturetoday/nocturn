// Package activity is the agent's live activity feed: a single ctx-carried sink
// that every producer of turn activity — the model's answer tokens and reasoning,
// the Registry's tool-call start/end — emits into, and the interactive session
// consumes. It is one-way observability, not domain data: the answer, tool results,
// and conversation flow through return values; this is only what the agent is DOING
// right now, surfaced live for a UI (a run with no sink is simply silent).
//
// It replaces three separate, inconsistent mechanisms that used to carry the same
// concept ("something happened, surface it"): the brain's OnToken field, the tool
// Registry's OnCall field, and the engine's re-adaptation of both. Unifying them at
// the source has three payoffs:
//
//   - the Brain and the Registry hold NO per-run output field, so they are immutable
//     and shared — never shallow-copied to vary a sink (the old `sub := *b` / `qb :=
//     *b` clones and the muted `quietReg` all disappear);
//   - reasoning ("thinking") finally has a home — it is just another variant, where
//     the two-field design had nowhere to put it;
//   - attach/detach falls out for free: a turn stamps one sink and any child it
//     spawns inherits it (its tokens, thinking, and tool calls nest into the parent),
//     while a detached/scheduled run carries no sink and is simply silent.
//
// Emit is fire-and-forget and one-way. A request-response need (a human approval that
// must return a decision) is a DIFFERENT seam — see the approval sink — never this one.
package activity

import "context"

// Event is one thing that happened during a turn. It is a closed union — type-switch
// on it. The unexported marker seals the set so only this package defines variants.
type Event interface{ isActivityEvent() }

// Token is one streamed chunk of the assistant's answer text.
type Token struct{ Text string }

// Thinking is one streamed chunk of the model's reasoning (extended thinking), kept
// distinct from the answer so a client can render or hide it separately.
type Thinking struct{ Text string }

// Phase marks whether a ToolEvent is the start or the end of an invocation.
type Phase int

const (
	Start Phase = iota
	End
)

// ToolEvent is emitted around every tool invocation — model- or script-issued. ID is
// unique per invocation and pairs a Start with its End; Parent is the enclosing
// invocation's ID (0 = root), so a consumer can reconstruct both concurrency
// (independent roots run at once) and nesting (a script's nocturn.call carries its
// code.run's ID as Parent). Because calls may run concurrently, events from different
// invocations interleave — match them by ID, never by arrival order.
type ToolEvent struct {
	ID     uint64 // unique per invocation
	Parent uint64 // enclosing invocation's ID; 0 = root
	Tool   string
	Args   string // JSON, as the caller supplied it (model args or script args)
	Phase  Phase
	Result string // End only
	Err    error  // End only (e.g. gateway.ErrDenied for a denied effect)
}

func (Token) isActivityEvent()     {}
func (Thinking) isActivityEvent()  {}
func (ToolEvent) isActivityEvent() {}

type sinkKey struct{}

// WithSink stamps sink onto ctx as the active activity sink for the turn. Every
// producer downstream (the model adapter's tokens/thinking, the Registry's tool
// events) emits into it via Emit. A child run started from this ctx inherits the
// sink — its activity nests into the parent turn.
func WithSink(ctx context.Context, sink func(Event)) context.Context {
	return context.WithValue(ctx, sinkKey{}, sink)
}

// SinkFrom returns the sink carried by ctx, or nil for a run with none (a detached /
// scheduled agent — its activity is simply not surfaced).
func SinkFrom(ctx context.Context) func(Event) {
	s, _ := ctx.Value(sinkKey{}).(func(Event))
	return s
}

// Emit delivers e to the sink on ctx if one is present, and is a no-op otherwise —
// so a producer never has to check for a sink, and a detached run stays silent.
func Emit(ctx context.Context, e Event) {
	if s := SinkFrom(ctx); s != nil {
		s(e)
	}
}
