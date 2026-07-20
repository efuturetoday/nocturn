package agentkit

import (
	"context"
	"time"
)

// The turn guards' internals. Wall-clock time and token spend are DEPLETABLE, GLOBAL resources
// carried in ctx and SHARED across nesting: a subagent spawned via AgentTool inherits the outer
// session's remaining time and token pool instead of starting its own. A session installs a guard
// into ctx only when none is present (it is the top-level run); an embedded run inherits the
// parent's. So a parent's WithTimeout / WithTokenLimit cap the parent AND all its subagents
// together. maxSteps is the exception — a per-run round-trip valve, counted fresh by each loop,
// never inherited.

// --- wall-clock (pausable) ---

// withTimeout installs a pausable wall-clock deadline of d, returning a cancel func. Time spent in
// a pause window (an out-of-band approval wait) does not count against it. A session calls this
// only if ctx carries no deadline yet, so an embedded run inherits the parent's remaining time.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	panic("TODO")
}

// pause stops the wall-clock while a blocking wait is in progress; resume restarts it. Token spend
// is unaffected.
func pause(ctx context.Context) (resume func()) { panic("TODO") }

// --- token spend (shared) ---

type tokenBudgetKey struct{}

// withTokenBudget installs a shared token-spend pool with the given limit (0 = unlimited) and
// returns ctx. A session calls this only if ctx carries no pool yet, so an embedded run draws from
// the parent's pool rather than starting a fresh allowance.
func withTokenBudget(ctx context.Context, limit int) context.Context { panic("TODO") }

// spend adds n tokens to the run's shared pool and reports whether the limit is now reached. No-op
// (never over) if no pool or no limit is set. The loop calls it with each round-trip's Total and
// stops with ErrTokenLimit when it reports true.
func spend(ctx context.Context, n int) (overLimit bool) { panic("TODO") }

// --- sub-agent spawn guards ---
//
// Nesting is bounded on THREE axes, because a depth cap alone is not enough — a depth-capped tree
// can still fan out to a huge descendant count. Depth is a per-branch value (deeper each level);
// the total spawn population is a SHARED counter across the whole tree (like token spend). Both,
// plus the inherited shared token/time budget above, cap a runaway tree. (A per-agent tool
// allowlist is the fourth axis and needs no ctx: an agent can only spawn the AgentTools present in
// its ToolSet — omit them and it is a leaf.)

type spawnKey struct{}

// withSpawnLimits installs the depth and population caps (0 = a sensible default), inherit-if-
// embedded like the budget — a session sets them only at the top level, nested runs inherit.
func withSpawnLimits(ctx context.Context, maxDepth, maxSpawns int) context.Context { panic("TODO") }

// enterSpawn is called by AgentTool's Call before running the sub-agent: it increments the branch
// depth and the shared total-spawn counter, returning ErrMaxDepth or ErrMaxSpawns if a cap would be
// exceeded (AgentTool surfaces that to the model as the tool result, so the model finishes the work
// directly instead of crashing). On success it returns a child ctx carrying the deeper level.
func enterSpawn(ctx context.Context) (context.Context, error) { panic("TODO") }
