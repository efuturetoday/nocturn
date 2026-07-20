package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
)

// Agent is an agent's declaration: a name, its instructions and its reasoning effort. It carries NO
// authority type (no policy/cage/capability), NO schedule, and NO tool list — scheduling is the
// consumer's business, and an agent's tools are chosen where it is wired (the ToolSet passed to
// AgentTool). Name must satisfy ToolNamePattern, since AgentTool uses it as the tool name.
type Agent struct {
	Name         string
	Instructions string
	Effort       Effort
}

// AgentTool exposes an agent as a callable Tool, so a parent agent can DELEGATE to it as a subagent —
// a subagent is just a tool. The returned tool's Call runs the agent to a final answer (Once) over
// the input argument, with a.Instructions as system and tools as its toolset. The caller scopes the
// subagent by what it passes: nil for a leaf, tools.Select(keep) for a subset, the full set
// otherwise — the same immutable-subset mechanism as everywhere else. Because Call receives the
// parent's ctx, the subagent's own tool calls nest under this call in the event forest, its
// tokens/events stream to the same sink, and it inherits the outer budget — nesting is automatic.
//
// Before running, Call passes through enterSpawn: it charges the tree's depth and population caps
// (WithMaxDepth / WithMaxSpawns) and, if a cap is exceeded, returns ErrMaxDepth / ErrMaxSpawns as the
// tool result so the model finishes the work directly instead of spawning further. Whether the
// subagent can spawn its OWN subagents is decided by whether any AgentTools are in tools — omit them
// and it is a leaf.
func AgentTool(a Agent, llm LLM, tools ToolSet, opts ...Option) Tool {
	return funcTool{
		spec: ToolSpec{
			Name:        a.Name,
			Description: fmt.Sprintf("Delegate a task to the %s sub-agent.", a.Name),
			Parameters:  Object(Prop("input", String("the task for the sub-agent"))).Require("input"),
		},
		fn: func(ctx context.Context, args string) (string, error) {
			ctx, err := enterSpawn(ctx)
			if err != nil {
				return "", err // ErrMaxDepth / ErrMaxSpawns — surfaced to the model as the tool result
			}
			var in struct {
				Input string `json:"input"`
			}
			if e := json.Unmarshal([]byte(args), &in); e != nil {
				return "", fmt.Errorf("%s: invalid arguments: %w", a.Name, e)
			}
			runOpts := append([]Option{
				WithSystem(a.Instructions),
				WithTools(tools),
				WithEffort(a.Effort),
			}, opts...)
			return Once(ctx, llm, in.Input, runOpts...)
		},
	}
}
