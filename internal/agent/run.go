package agent

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/deadline"
	"github.com/efuturetoday/nocturn/internal/skill"
)

// RunTask runs one task of def to a final answer, composing on ctx:
//   - a fresh epoch + grant set scoped to "agent:<name>" — so this agent's
//     "always for this agent" approvals are ITS OWN and never leak to the
//     interactive session or another agent; closing the epoch revokes its
//     run-scoped grants.
//   - an optional wall-clock budget (pausable: it does not drain while a HITL
//     approval is pending).
//   - a brain limited to the agent's OWN tools via a filtered Registry — a tool
//     outside the list is UNREACHABLE, not merely hidden from the model. Skills
//     are opt-in like any tool (add "skill" to Tools): off by default so a focused
//     agent carries no skill-catalog context. skill.WithActive is stamped anyway —
//     harmless when no skill tool is present, correct when one is.
//
// Every effect is gated exactly as everywhere else (broker + HITL); with a human
// present, an out-of-scope effect asks. The derived auto/ask envelope for
// UNATTENDED (scheduled) runs is the next shell.
func RunTask(ctx context.Context, b *brain.Brain, epochs *capability.EpochRegistry, store capability.GrantStore, def Definition, task string) (string, error) {
	epoch := epochs.Open()
	defer epochs.Close(epoch)

	grants := capability.NewGrants("agent:"+def.Name, epoch, store)
	ctx = capability.WithGrants(ctx, grants)
	ctx = skill.WithActive(ctx, skill.NewActive()) // fresh skill.load dedup for this run
	if def.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = deadline.WithBudget(ctx, def.Budget)
		defer cancel()
	}

	sub := *b
	sub.Registry = b.Registry.Select(def.Matches)
	return sub.Run(ctx, def.Instructions+"\n\n---\nTask:\n"+task)
}
