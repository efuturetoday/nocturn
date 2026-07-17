package agent

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/deadline"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Deps is the shared machinery a child-agent run needs, bundled so Run's signature
// stays small as the subsystem grows (no loose epochs/store arguments). Brain is the
// shared stateless executor; Tools is the FULL workspace registry (Run filters it to
// the agent's own tools); Guard owns the permission-scope lifecycle; Store resolves an
// agent's OWN durable "always" backing by name (nil, or a resolver returning nil,
// means no persistence — its always-grants do not survive the process).
type Deps struct {
	Brain *brain.Brain
	Tools *tool.Registry
	Guard *gateway.Guard
	Store func(name string) capability.GrantStore
}

// Result is what one agent run produces. A struct (not a bare string) so callers
// survive added fields — token counts, tool tallies — without a signature change.
type Result struct {
	Answer string
}

// Turn is THE execution — the one place the scoped-run ceremony lives. It binds the
// scope, composes agent a's OWN restrictions onto ctx (policy: tightening deny/ask;
// cage: intersecting reach bound; budget: a pausable wall-clock deadline), stamps the
// active-skills set, and drives the brain loop over conv (which already holds a's tool
// subset). A child Run and an interactive Session turn are the SAME operation through
// here — they differ only in their inputs:
//
//	Run      → a declared Agent, a FRESH conversation over its FILTERED tools, run once.
//	Session  → the empty root Agent{} (no restrictions, FULL tools), a PERSISTENT
//	           conversation, run per user turn.
//
// The scope is bound but NOT opened here — its lifetime (per-run vs. session-persistent)
// belongs to the caller. Every effect is gated exactly as everywhere else (broker +
// HITL); ctx carries the activity + approval sinks, so an attended spawn nests into the
// parent chat and a detached run resolves out of band by its autonomy dial.
func Turn(ctx context.Context, scope *gateway.Scope, active *skill.Active, a Agent, conv *brain.Conversation, input string) (string, error) {
	ctx = scope.Bind(ctx)
	// Author config, never grants (KONZEPT §9). Empty on the root agent → skipped.
	if len(a.Policy.Rules) > 0 {
		ctx = capability.WithPolicy(ctx, a.Policy)
	}
	if len(a.Cage) > 0 {
		ctx = capability.WithCage(ctx, capability.NewCage(a.Cage...))
	}
	ctx = skill.WithActive(ctx, active)
	if a.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = deadline.WithBudget(ctx, a.Budget)
		defer cancel()
	}
	return conv.Send(ctx, input)
}

// Run runs one task of agent a to a final answer: it opens a's OWN revocable scope over
// its OWN durable store (so its "always for this agent" grants never leak to the session
// or another agent — closing the scope drops its session grants), builds a FRESH
// conversation over the agent's tool subset (a tool outside a.Tools is UNREACHABLE, not
// merely hidden), and runs one shared Turn. skill.load dedup is fresh per run — skills
// are opt-in like any tool (add "skill" to Tools), off by default for a focused agent.
func Run(ctx context.Context, d Deps, a Agent, task string) (Result, error) {
	var store capability.GrantStore
	if d.Store != nil {
		store = d.Store(a.Name)
	}
	scope := d.Guard.NewScope(store)
	defer scope.Revoke()

	conv := d.Brain.NewConversation(d.Tools.Select(a.Matches))
	answer, err := Turn(ctx, scope, skill.NewActive(), a, conv, a.Instructions+"\n\n---\nTask:\n"+task)
	return Result{Answer: answer}, err
}
