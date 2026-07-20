package agentkit

import "context"

// LLM is the model port: given the conversation and the available tool specs, produce the
// next step (a final answer or a batch of tool calls). Streaming deltas are emitted on the
// ctx event sink by the adapter.
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
