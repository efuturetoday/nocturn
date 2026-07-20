package agentkit

import "context"

// LLM is the model port: given the conversation and the available tool specs, produce the next step
// (a final answer or a batch of tool calls). Streaming deltas are emitted on the ctx event sink by
// the adapter.
type LLM interface {
	Next(ctx context.Context, conv []Message, tools []ToolSpec) (Step, error)
}

// Effort is an opaque reasoning-effort dial the adapter maps to provider knobs.
type Effort string

const (
	EffortNone   Effort = ""
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

type effortKey struct{}

// withEffort carries the session's reasoning effort down to the adapter for this turn.
func withEffort(ctx context.Context, e Effort) context.Context {
	if e == "" {
		return ctx
	}
	return context.WithValue(ctx, effortKey{}, e)
}

// EffortFrom returns the reasoning effort carried in ctx (set by the session), or "" if none. An LLM
// adapter reads it to override its own default per turn.
func EffortFrom(ctx context.Context) Effort {
	e, _ := ctx.Value(effortKey{}).(Effort)
	return e
}
