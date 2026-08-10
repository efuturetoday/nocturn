package workspace

import (
	"fmt"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/memory"
	"github.com/efuturetoday/nocturn/internal/tools"
	"slices"
)

// This file is how a workspace's tools become CAGES. buildTools composes the root chat's set; every
// declared agent gets its own, composed from the base rather than selected out of an already-composed
// one — see agentCage for why that distinction is the whole authority story.

// buildTools assembles the workspace toolset: the base tools plus code_run (the root chat's cage),
// and each declared agent exposed as a sub-agent tool scoped to its OWN cage — its filtered subset of
// the base tools, plus code_run only if the agent declares it, dispatching over that same subset.
// code_run is woven per cage (tools.Compose), so a script never reaches past the cage it runs in.
func buildTools(
	base agentkit.ToolSet,
	llm agentkit.LLM,
	agents agent.Set,
	mem *memory.Store,
) (agentkit.ToolSet, error) {
	// Root chat cage: every base tool + code_run dispatching over them.
	rootSet, err := tools.Compose(base, true)
	if err != nil {
		return agentkit.ToolSet{}, err
	}
	all := make([]agentkit.Tool, 0, len(rootSet)+len(agents.All()))
	for _, t := range rootSet {
		all = append(all, t)
	}

	for _, a := range agents.All() {
		cage, err := agentCage(base, a)
		if err != nil {
			return agentkit.ToolSet{}, err
		}
		// The sub-agent's prompt carries the memory index too, but only the part of it its own cage
		// can maintain — AgentTool applies caller options after its own, so this wins over the
		// WithSystem(a.Instructions) it sets internally.
		sub := agentkit.AgentTool(
			agentkit.Agent{Name: a.Name, Instructions: a.Instructions, Effort: a.Effort},
			llm, cage,
			agentkit.WithSystemFunc(func() string { return composePrompt(a.Instructions, mem, a.Matches) }),
		)
		all = append(all, sub)
	}
	return agentkit.NewToolSet(all...)
}

// agentCage is the toolset one declared agent may act through: its filtered subset of the base
// tools, plus code_run only when it declares it, dispatching over exactly that subset.
//
// It is COMPOSED from base rather than selected out of an already-composed set, and that is the
// whole authority story. Selecting by name from the workspace toolset would find the root code_run
// — whose dispatch set is the full base, captured when it was built — so an agent declaring
// [file_read, code_run] would list two tools and have a script that reaches file_write, http_write
// and memory_write. Both the sub-agent tool and a fired run go through here, so the cage cannot
// differ between the two ways of reaching the same agent.
// The name goes on the error here rather than at either call site, so neither can forget it: a
// workspace with five agents that fails to compose one of them has to say which.
func agentCage(base agentkit.ToolSet, a agent.Agent) (agentkit.ToolSet, error) {
	cage, err := tools.Compose(base.Select(a.Matches), a.Matches(tools.CodeRunTool))
	if err != nil {
		return agentkit.ToolSet{}, fmt.Errorf("agent %q: cage: %w", a.Name, err)
	}
	return cage, nil
}

// toolNames lists a toolset's tools, sorted — a map has no order, and a list that reshuffles itself
// between two looks is unreadable.
func toolNames(ts agentkit.ToolSet) []string {
	out := make([]string, 0, len(ts))
	for _, s := range ts.Specs() {
		out = append(out, s.Name)
	}
	slices.Sort(out)
	return out
}
