// Package agent is the declaration layer for autonomous assistants: the Agent type, discovery of
// agent.md files into an immutable Set, and a cron Scheduler. It depends on nothing from the
// composition layer (no runtime, gate, or chat) — execution (FireAgent) lives in the workspace,
// which injects a fire callback into the Scheduler.
package agent

import (
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
)

// Autonomy dictates how a SCHEDULED firing resolves an Ask — the one moment no human is in the loop
// by default. It is inert for in-chat sub-agent calls, which inherit the caller's gate ctx (approver
// already present) and are attended by construction.
type Autonomy string

const (
	// Strict runs with no approver: any fresh Ask is denied (gate.ErrDeniedUnattended). The zero
	// value resolves here, so a missing or typo'd dial never silently escalates authority.
	Strict Autonomy = "strict"
	// Guarded routes an Ask out-of-band to the human approver (the phone) — nocturn's thesis applied
	// to a background agent: it may reach past its standing grants, but only with a human's consent.
	Guarded Autonomy = "guarded"
)

// Agent is a declared assistant: a scoped persona that runs on a schedule or on demand. Its
// authority is its tool cage (Tools → ToolSet.Select) plus the workspace gate; a scheduled firing's
// Autonomy decides whether a fresh Ask reaches a human or is denied.
type Agent struct {
	Name         string
	Description  string          // one-line summary for listing/selection
	Instructions string          // the markdown body: the agent's system prompt / task framing
	Tools        []string        // tool-name filter for the cage; empty = a pure reasoner
	When         string          // cron schedule; "" = manual only
	Effort       agentkit.Effort // reasoning effort for the agent's runs
	Budget       time.Duration   // per-run wall-clock; 0 = the workspace default
	Autonomy     Autonomy        // how a scheduled firing resolves an Ask
}

// Matches reports whether toolName is in the agent's cage. A bare group name ("http") also matches
// its members under any of the separators tool names use: "http_read" (underscore, the agentkit
// convention), "http.read", or "http/get".
func (a Agent) Matches(toolName string) bool {
	for _, t := range a.Tools {
		if t == toolName ||
			strings.HasPrefix(toolName, t+"_") ||
			strings.HasPrefix(toolName, t+".") ||
			strings.HasPrefix(toolName, t+"/") {
			return true
		}
	}
	return false
}
