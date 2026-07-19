package brain

import (
	"context"
	"strings"
)

// Effort is the model's reasoning-effort level (the OpenAI reasoning_effort knob). The empty
// value means UNSET — the endpoint's own default — and is omitted from the request. Non-reasoning
// models ignore it.
type Effort string

const (
	EffortMinimal Effort = "minimal" // gpt-5-era, endpoint-dependent
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh" // not OpenAI-standard; passed through if the endpoint accepts it
)

// ParseEffort validates an untrusted string (a wire field or agent frontmatter) into an Effort: an
// unknown value becomes "" (unset), never an error — a typo degrades to the default rather than
// failing the turn. The set is the known levels; the endpoint is the final judge of any it accepts.
func ParseEffort(s string) Effort {
	switch e := Effort(strings.ToLower(strings.TrimSpace(s))); e {
	case EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh:
		return e
	default:
		return ""
	}
}

type effortKey struct{}

// WithEffort stamps the reasoning effort for ONE turn onto ctx; the model adapter reads it at the
// request boundary. Per-turn (like the activity sink), so a per-message override never leaks into
// the next turn.
func WithEffort(ctx context.Context, e Effort) context.Context {
	return context.WithValue(ctx, effortKey{}, e)
}

// EffortFrom returns the effort carried by ctx, or "" (unset) on a bare context.
func EffortFrom(ctx context.Context) Effort {
	e, _ := ctx.Value(effortKey{}).(Effort)
	return e
}
