// Package agent is the declaration layer for autonomous assistants: the Agent type, discovery of
// agent.md files into an immutable Set, and a cron Scheduler. It depends on nothing from the
// composition layer (no runtime, gate, or chat) — execution (FireAgent) lives in the workspace,
// which injects a fire callback into the Scheduler.
package agent

import (
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
)

// Agent is a declared assistant: a scoped persona that runs on a schedule or on demand. Its
// authority is its tool cage (Tools → ToolSet.Select) plus the workspace gate; an unattended firing
// gets no approver, so anything not already granted is denied.
type Agent struct {
	Name         string
	Instructions string
	Tools        []string        // tool-name filter for the cage; empty = a pure reasoner
	When         string          // cron schedule; "" = manual only
	Effort       agentkit.Effort // reasoning effort for the agent's runs
}

// Matches reports whether toolName is in the agent's cage. A bare group name ("http") also matches
// its members ("http.read", "http/get").
func (a Agent) Matches(toolName string) bool {
	for _, t := range a.Tools {
		if t == toolName || strings.HasPrefix(toolName, t+".") || strings.HasPrefix(toolName, t+"/") {
			return true
		}
	}
	return false
}
