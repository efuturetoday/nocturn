package main

import (
	"fmt"
	"strings"

	"github.com/efuturetoday/nocturn/internal/agent"
)

// reportAgents prints a one-line startup summary of the workspace agents, before
// the TUI. No prompt — defining an agent grants nothing; running one is an
// explicit user action (/<name> <task>), gated per effect like any turn.
func reportAgents(defs []agent.Agent) {
	if len(defs) == 0 {
		return
	}
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = "/" + d.Name
	}
	fmt.Printf("Agents: %s — run with /<name> <task>, list with /agents.\n", strings.Join(names, " "))
}
