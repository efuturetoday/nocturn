package agentkit

// Agent is an agent's declaration: a named, instruction- and tool-scoped session config. It
// carries NO authority type (no policy/cage/capability) and NO schedule — attenuation and
// scheduling are the consumer's business. Run one with Once, or expose it as a subagent tool with
// AgentTool.
type Agent struct {
	Name         string
	Instructions string
	Tools        func(name string) bool // filter selecting the agent's tools from a base ToolSet
	Effort       Effort
}

// Matches reports whether this agent may use the named tool.
func (a Agent) Matches(name string) bool { panic("TODO") }

// AgentTool exposes an agent as a callable Tool, so a parent agent can DELEGATE to it as a
// subagent — a subagent is just a tool. The returned tool's Call runs the agent to a final answer
// (Once) over the input argument, with a.Instructions as system and tools.Select(a.Matches) as its
// toolset. Because Call receives the parent's ctx, the subagent's own tool calls nest under this
// call in the event forest, its tokens/events stream to the same sink, and it inherits the outer
// budget — nesting is automatic.
//
// Before running, Call passes through enterSpawn: it charges the tree's depth and population caps
// (WithMaxDepth / WithMaxSpawns) and, if a cap is exceeded, returns ErrMaxDepth / ErrMaxSpawns as
// the tool result so the model finishes the work directly instead of spawning further. Whether the
// subagent can spawn its OWN subagents is decided by whether any AgentTools are in tools.Select(
// a.Matches) — omit them and it is a leaf.
func AgentTool(a Agent, llm LLM, tools ToolSet, opts ...Option) Tool { panic("TODO") }
