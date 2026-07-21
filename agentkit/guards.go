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
//
// A ctx carries a SET of pausable wall-clock deadlines: the turn's, plus any NESTED budget that also
// wants to pause while parked on an approval — e.g. a sandbox guest run (WithPausableBudget). Pause
// pauses them ALL, so an out-of-band human decision never trips the turn deadline NOR the inner
// guest budget. When a deadline fires it cancels its ctx with cause ErrTurnTimeout, so the turn can
// report a clear "timed out" instead of a bare "context canceled".

type pausablesKey struct{}

// pausables is the shared set of active deadlines on a ctx. The turn creates it; a nested budget
// appends to it (so Pause reaches both). Each deadline owns its own timer and ctx.
type pausables struct {
	mu    sync.Mutex
	items []*deadline
}

type deadline struct {
	mu        sync.Mutex
	timer     *time.Timer
	cancel    context.CancelCauseFunc
	at        time.Time     // when it fires (while running)
	remaining time.Duration // time left (while paused)
	paused    bool
}

// installDeadline adds a pausable deadline of d to ctx (creating the shared set if this is the first),
// cancelling ctx with cause ErrTurnTimeout when it fires. Returns ctx + a stop func that cancels and
// removes it.
func installDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	dl := &deadline{cancel: cancel, at: time.Now().Add(d)}
	dl.timer = time.AfterFunc(d, func() { cancel(ErrTurnTimeout) })

	set, _ := ctx.Value(pausablesKey{}).(*pausables)
	if set == nil {
		set = &pausables{}
		ctx = context.WithValue(ctx, pausablesKey{}, set)
	}
	set.mu.Lock()
	set.items = append(set.items, dl)
	set.mu.Unlock()

	return ctx, func() {
		dl.timer.Stop()
		cancel(context.Canceled)
		set.mu.Lock()
		for i, x := range set.items {
			if x == dl {
				set.items = append(set.items[:i], set.items[i+1:]...)
				break
			}
		}
		set.mu.Unlock()
	}
}

// withTimeout installs the TURN's pausable deadline (d <= 0 = none). A session calls it only if ctx
// carries no deadline set yet, so an embedded run inherits the parent's remaining time.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 || ctx.Value(pausablesKey{}) != nil {
		return context.WithCancel(ctx)
	}
	return installDeadline(ctx, d)
}

// WithPausableBudget adds a pausable wall-clock cap of d (d <= 0 = none) that ALSO pauses (via Pause)
// while parked on an approval — for a nested budget such as a sandbox guest run, whose real-execution
// cap must not count out-of-band wait time. Unlike the turn deadline it does NOT inherit; each call
// adds its own to the set. Returns ctx + a stop func.
func WithPausableBudget(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return installDeadline(ctx, d)
}

// Pause stops EVERY pausable deadline on ctx while a blocking wait (e.g. an out-of-band approval) is
// in progress, returning a resume that restarts them all. A tool's Call invokes it so a human
// deciding never trips a wall-clock cap. No-op if ctx carries none. Token spend is unaffected. The
// paused span is also banked on the ctx's pausedClock so a tool's reported duration can exclude the
// out-of-band wait (see activeSince).
func Pause(ctx context.Context) (resume func()) {
	clock, _ := ctx.Value(pausedClockKey{}).(*pausedClock)
	start := time.Now()

	set, _ := ctx.Value(pausablesKey{}).(*pausables)
	var resumes []func()
	if set != nil {
		set.mu.Lock()
		items := append([]*deadline(nil), set.items...) // snapshot; don't hold the set lock while pausing
		set.mu.Unlock()
		resumes = make([]func(), 0, len(items))
		for _, dl := range items {
			resumes = append(resumes, dl.pause())
		}
	}
	return func() {
		for _, r := range resumes {
			r()
		}
		if clock != nil {
			clock.nanos.Add(int64(time.Since(start)))
		}
	}
}

// --- paused-time accounting (shared) ---
//
// A ctx carries a single monotonic counter of nanoseconds spent parked in Pause (out-of-band
// approvals). It is installed once at the top level and inherited by nested runs, so every tool
// call — parent or nested — reads the SAME counter. A call snapshots it at start and end; the delta
// is exactly the approval wait that overlapped that call, which it subtracts from its wall-clock so
// the reported duration is active execution time, not human-decision time.

type pausedClockKey struct{}

type pausedClock struct{ nanos atomic.Int64 }

// withPausedClock installs the shared paused-time counter (inherit-if-present, like the token pool).
func withPausedClock(ctx context.Context) context.Context {
	if ctx.Value(pausedClockKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, pausedClockKey{}, &pausedClock{})
}

// pausedNanos reports the ctx's total banked paused time so far (0 if none installed).
func pausedNanos(ctx context.Context) int64 {
	c, _ := ctx.Value(pausedClockKey{}).(*pausedClock)
	if c == nil {
		return 0
	}
	return c.nanos.Load()
}

// activeSince returns the wall-clock elapsed since start MINUS any approval wait banked over that
// span (pausedStart = pausedNanos(ctx) captured at start). Never negative.
func activeSince(ctx context.Context, start time.Time, pausedStart int64) time.Duration {
	d := time.Since(start) - time.Duration(pausedNanos(ctx)-pausedStart)
	if d < 0 {
		return 0
	}
	return d
}

// pause stops one deadline's timer, banking its remaining time, and returns a resume that restarts it.
func (dl *deadline) pause() (resume func()) {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	if dl.paused {
		return func() {}
	}
	dl.paused = true
	if dl.timer.Stop() {
		dl.remaining = max(time.Until(dl.at), 0)
	} else {
		dl.remaining = 0 // already fired
	}
	return func() {
		dl.mu.Lock()
		defer dl.mu.Unlock()
		if !dl.paused {
			return
		}
		dl.paused = false
		dl.at = time.Now().Add(dl.remaining)
		dl.timer.Reset(dl.remaining)
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
