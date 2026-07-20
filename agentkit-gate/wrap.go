package gate

import (
	"context"

	"github.com/efuturetoday/agentkit"
)

// Wrap decorates a tool so every call is gated on its name (Action{Tool: name}) before it runs. Use
// it for name-only tools (notify, delete, exec). A tool that reaches a runtime target — a host, a
// path — instead calls Check itself with that Target, so a host allowlist is gated too.
func Wrap(t agentkit.Tool) agentkit.Tool { return gated{t} }

// WrapAll wraps every tool in a set, returning a new set (the originals are unchanged).
func WrapAll(ts agentkit.ToolSet) agentkit.ToolSet {
	out := make(agentkit.ToolSet, len(ts))
	for name, t := range ts {
		out[name] = gated{t}
	}
	return out
}

// gated is a tool whose Call is gated on the tool name.
type gated struct {
	agentkit.Tool
}

func (g gated) Call(ctx context.Context, args string) (string, error) {
	if err := Check(ctx, Action{Tool: g.Spec().Name}); err != nil {
		return "", err
	}
	return g.Tool.Call(ctx, args)
}
