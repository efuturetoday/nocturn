// Package deadline provides an execution-time budget carried in a context whose
// deadline can be PAUSED and resumed. It exists so that time spent parked waiting
// for an out-of-band human approval (HITL) does not consume the guest/tool
// execution budget: the wait is bounded by the HITL TTL, and the budget resumes
// with its remaining allowance once the human answers.
//
// A budget behaves like context.WithTimeout but is discoverable via PauserFrom
// and pausable, and it chains to any budget already on the parent context — so
// pausing the innermost budget pauses the whole enclosing chain (e.g. the
// sandbox guest deadline and the brain per-tool timeout together).
//
// A script can extend its own wall-clock lifetime by triggering repeated
// approvals (each pauses the budget for up to the HITL TTL). That is the intended
// contract: the human — and the capability rate limiter — is the throttle, not a
// fixed deadline that would trap a run mid-approval. Only real cancellation
// (Esc/Ctrl-C, TTL expiry) still aborts immediately; pausing stops the deadline
// timer, never the context's cancellation propagation.
package deadline

import (
	"context"
	"sync"
	"time"
)

// Pauser suspends and resumes the execution deadline(s) governing a context.
// Pause and Resume must be balanced; an unbalanced Resume is a no-op.
type Pauser interface {
	Pause()
	Resume()
}

type budgetKey struct{}

// WithBudget returns a child context cancelled after d of UNPAUSED time, plus a
// CancelFunc. On expiry the context's cause is context.DeadlineExceeded (read it
// with context.Cause, not ctx.Err, which reports context.Canceled); the returned
// CancelFunc cancels with context.Canceled. The budget is discoverable via
// PauserFrom and chains to any budget already on parent.
func WithBudget(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	cctx, cancel := context.WithCancelCause(parent)
	b := &budget{
		parent:    PauserFrom(parent), // may be nil
		cancel:    cancel,
		remaining: d, // explicit: a zero here would make the first Pause() Reset(0)
		startedAt: time.Now(),
	}
	b.timer = time.AfterFunc(d, func() { cancel(context.DeadlineExceeded) })
	ctx := context.WithValue(cctx, budgetKey{}, b)
	return ctx, func() {
		b.mu.Lock()
		b.timer.Stop()
		b.mu.Unlock()
		cancel(context.Canceled)
	}
}

// PauserFrom returns the innermost budget on ctx, or nil if none. It returns an
// untyped nil, so callers can safely test the result with != nil.
func PauserFrom(ctx context.Context) Pauser {
	p, _ := ctx.Value(budgetKey{}).(Pauser)
	return p
}

type budget struct {
	parent Pauser
	cancel context.CancelCauseFunc

	mu        sync.Mutex
	timer     *time.Timer
	remaining time.Duration
	startedAt time.Time
	depth     int
}

// Pause stops the deadline timer (charging the elapsed run time against the
// remaining budget) and propagates up the chain. Nested pauses are counted.
func (b *budget) Pause() {
	b.mu.Lock()
	b.depth++
	if b.depth == 1 {
		if b.timer.Stop() {
			b.remaining -= time.Since(b.startedAt)
			if b.remaining < 0 {
				b.remaining = 0
			}
		}
		// Stop()==false means the timer already fired (the context is already
		// cancelled) — nothing to charge; a later Resume's Reset is a no-op on a
		// dead context.
	}
	b.mu.Unlock()
	if b.parent != nil {
		b.parent.Pause()
	}
}

// Resume restarts the deadline timer with the remaining budget once the last
// matching Pause is released, and propagates up the chain. An unbalanced Resume
// (no matching Pause) is a no-op and does not touch the parent.
func (b *budget) Resume() {
	b.mu.Lock()
	if b.depth == 0 {
		b.mu.Unlock()
		return
	}
	b.depth--
	if b.depth == 0 {
		b.startedAt = time.Now()
		b.timer.Reset(b.remaining)
	}
	b.mu.Unlock()
	if b.parent != nil {
		b.parent.Resume()
	}
}
