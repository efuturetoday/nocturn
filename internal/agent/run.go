package agent

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/deadline"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/skill"
)

// Deps is the shared machinery a child-agent run needs, bundled so Run's signature
// stays small as the subsystem grows (no loose epochs/store arguments). Brain is the
// shared stateless executor; Guard owns the permission-scope lifecycle; Store resolves
// an agent's OWN durable "always" backing by name (nil, or a resolver returning nil,
// means no persistence — its always-grants do not survive the process).
type Deps struct {
	Brain *brain.Brain
	Guard *gateway.Guard
	Store func(name string) capability.GrantStore
}

// Result is what one agent run produces. A struct (not a bare string) so callers
// survive added fields — token counts, tool tallies — without a signature change.
type Result struct {
	Answer string
}

// Run runs one task of a to a final answer, composing on ctx:
//   - a fresh gateway.Scope over the agent's OWN durable store (Deps.Store(a.Name)) —
//     so this agent's "always for this agent" approvals are ITS OWN and never leak to
//     the interactive session or another agent; the scope's epoch is revoked when the
//     run ends, dropping its session-scoped grants. The Guard owns the epoch registry,
//     so the run never touches it directly.
//   - an optional wall-clock budget (pausable: it does not drain while a HITL approval
//     is pending).
//   - a brain limited to the agent's OWN tools via a filtered Registry — a tool outside
//     the list is UNREACHABLE, not merely hidden from the model. Skills are opt-in like
//     any tool (add "skill" to Tools): off by default so a focused agent carries no
//     skill-catalog context. skill.WithActive is stamped anyway — harmless when no skill
//     tool is present, correct when one is.
//
// Every effect is gated exactly as everywhere else (broker + HITL). An attended spawn
// inherits the parent's activity + approval sinks from ctx (its tokens and prompts nest
// into the parent chat); a detached/scheduled run carries neither and is resolved by its
// autonomy dial out of band.
func Run(ctx context.Context, d Deps, a Agent, task string) (Result, error) {
	var store capability.GrantStore
	if d.Store != nil {
		store = d.Store(a.Name)
	}
	scope := d.Guard.NewScope(store)
	defer scope.Revoke()
	ctx = scope.Bind(ctx)
	// The agent author's own scope: its policy (tightening: deny blacklist / force-ask)
	// composes with the workspace base, and its optional cage intersects any outer
	// bound — author config, never grants (KONZEPT §9).
	if len(a.Policy.Rules) > 0 {
		ctx = capability.WithPolicy(ctx, a.Policy)
	}
	if len(a.Cage) > 0 {
		ctx = capability.WithCage(ctx, capability.NewCage(a.Cage...))
	}
	ctx = skill.WithActive(ctx, skill.NewActive()) // fresh skill.load dedup for this run
	if a.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = deadline.WithBudget(ctx, a.Budget)
		defer cancel()
	}

	sub := *d.Brain
	sub.Registry = d.Brain.Registry.Select(a.Matches)
	answer, err := sub.Run(ctx, a.Instructions+"\n\n---\nTask:\n"+task)
	return Result{Answer: answer}, err
}
