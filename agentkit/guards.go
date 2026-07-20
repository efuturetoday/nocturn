package agentkit

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Sensible defaults applied when an option is left at 0.
const (
	defaultMaxSteps  = 16
	defaultMaxDepth  = 4
	defaultMaxSpawns = 64
)

// The turn guards' internals. Wall-clock time and token spend are DEPLETABLE, GLOBAL resources
// carried in ctx and SHARED across nesting: a subagent spawned via AgentTool inherits the outer
// session's remaining time and token pool instead of starting its own. A session installs a guard
// into ctx only when none is present (it is the top-level run); an embedded run inherits the
// parent's. So a parent's WithTimeout / WithTokenLimit cap the parent AND all its subagents
// together. maxSteps is the exception — a per-run round-trip valve, counted fresh by each loop,
// never inherited.

// --- wall-clock (pausable) ---

type timeoutKey struct{}

type timeoutState struct {
	mu        sync.Mutex
	timer     *time.Timer
	cancel    context.CancelFunc
	deadline  time.Time     // when the timer fires (while running)
	remaining time.Duration // time left (while paused)
	paused    bool
}

// withTimeout installs a pausable wall-clock deadline of d (d <= 0 = no timeout), returning a cancel
// func that stops the timer. A session calls this only if ctx carries no timeout yet, so an embedded
// run inherits the parent's remaining time.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 || ctx.Value(timeoutKey{}) != nil {
		return context.WithCancel(ctx)
	}
	ctx, cancel := context.WithCancel(ctx)
	st := &timeoutState{cancel: cancel, deadline: time.Now().Add(d)}
	st.timer = time.AfterFunc(d, cancel)
	ctx = context.WithValue(ctx, timeoutKey{}, st)
	return ctx, func() {
		st.timer.Stop()
		cancel()
	}
}

// Pause stops the wall-clock while a blocking wait (e.g. an out-of-band approval) is in progress and
// returns a resume func to restart it. A tool's Call invokes it so a human deciding never trips the
// turn timeout. No-op (resume is a no-op) if ctx carries no pausable timeout. Token spend is
// unaffected.
func Pause(ctx context.Context) (resume func()) {
	st, _ := ctx.Value(timeoutKey{}).(*timeoutState)
	if st == nil {
		return func() {}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.paused {
		return func() {}
	}
	st.paused = true
	if st.timer.Stop() {
		st.remaining = max(time.Until(st.deadline), 0)
	} else {
		st.remaining = 0 // already fired
	}
	return func() {
		st.mu.Lock()
		defer st.mu.Unlock()
		if !st.paused {
			return
		}
		st.paused = false
		st.deadline = time.Now().Add(st.remaining)
		st.timer.Reset(st.remaining)
	}
}

// --- token spend (shared) ---

type tokenBudgetKey struct{}

type tokenBudget struct {
	limit int
	used  atomic.Int64
}

// withTokenBudget installs a shared token-spend pool with the given limit (0 = unlimited) and
// returns ctx. A session calls this only if ctx carries no pool yet, so an embedded run draws from
// the parent's pool rather than starting a fresh allowance.
func withTokenBudget(ctx context.Context, limit int) context.Context {
	if ctx.Value(tokenBudgetKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, tokenBudgetKey{}, &tokenBudget{limit: limit})
}

// spend adds n tokens to the run's shared pool and reports whether the limit is now reached. No-op
// (never over) if no pool or no limit is set. The loop calls it with each round-trip's Total and
// stops with ErrTokenLimit when it reports true.
func spend(ctx context.Context, n int) (overLimit bool) {
	b, _ := ctx.Value(tokenBudgetKey{}).(*tokenBudget)
	if b == nil || b.limit <= 0 {
		return false
	}
	return int(b.used.Add(int64(n))) >= b.limit
}

// --- sub-agent spawn guards ---
//
// Nesting is bounded on THREE axes, because a depth cap alone is not enough — a depth-capped tree
// can still fan out to a huge descendant count. Depth is a per-branch value (deeper each level); the
// total spawn population is a SHARED counter across the whole tree (like token spend). Both, plus the
// inherited shared token/time budget above, cap a runaway tree. (A per-agent tool allowlist is the
// fourth axis and needs no ctx: an agent can only spawn the AgentTools present in its ToolSet.)

type spawnKey struct{}
type spawnDepthKey struct{}

type spawnState struct {
	maxDepth  int
	maxSpawns int
	spawned   atomic.Int64
}

// withSpawnLimits installs the depth and population caps (0 = a sensible default), inherit-if-
// embedded like the budget — a session sets them only at the top level, nested runs inherit.
func withSpawnLimits(ctx context.Context, maxDepth, maxSpawns int) context.Context {
	if ctx.Value(spawnKey{}) != nil {
		return ctx
	}
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	if maxSpawns <= 0 {
		maxSpawns = defaultMaxSpawns
	}
	return context.WithValue(ctx, spawnKey{}, &spawnState{maxDepth: maxDepth, maxSpawns: maxSpawns})
}

// enterSpawn is called by AgentTool's Call before running the sub-agent: it increments the branch
// depth and the shared total-spawn counter, returning ErrMaxDepth or ErrMaxSpawns if a cap would be
// exceeded (AgentTool surfaces that to the model as the tool result). On success it returns a child
// ctx carrying the deeper level.
func enterSpawn(ctx context.Context) (context.Context, error) {
	st, _ := ctx.Value(spawnKey{}).(*spawnState)
	if st == nil {
		return ctx, nil
	}
	depth, _ := ctx.Value(spawnDepthKey{}).(int)
	depth++
	if st.maxDepth > 0 && depth > st.maxDepth {
		return ctx, ErrMaxDepth
	}
	if st.maxSpawns > 0 && int(st.spawned.Add(1)) > st.maxSpawns {
		return ctx, ErrMaxSpawns
	}
	return context.WithValue(ctx, spawnDepthKey{}, depth), nil
}
