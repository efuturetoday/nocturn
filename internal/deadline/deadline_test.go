package deadline_test

import (
	"context"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/deadline"
)

// doneWithin reports whether ctx is done within d.
func doneWithin(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func TestBudget_FiresAfterDuration(t *testing.T) {
	ctx, cancel := deadline.WithBudget(context.Background(), 60*time.Millisecond)
	defer cancel()

	if doneWithin(ctx, 20*time.Millisecond) {
		t.Fatal("budget fired too early")
	}
	if !doneWithin(ctx, 300*time.Millisecond) {
		t.Fatal("budget never fired")
	}
	if context.Cause(ctx) != context.DeadlineExceeded {
		t.Fatalf("cause = %v, want DeadlineExceeded", context.Cause(ctx))
	}
}

func TestBudget_PauseFreezesClock(t *testing.T) {
	ctx, cancel := deadline.WithBudget(context.Background(), 80*time.Millisecond)
	defer cancel()

	p := deadline.PauserFrom(ctx)
	if p == nil {
		t.Fatal("PauserFrom returned nil for a budget context")
	}
	p.Pause()
	// Well past the 80ms budget — but paused, so it must not fire.
	if doneWithin(ctx, 250*time.Millisecond) {
		t.Fatal("budget fired while paused")
	}
	p.Resume()
	if !doneWithin(ctx, 400*time.Millisecond) {
		t.Fatal("budget did not resume after Resume")
	}
	if context.Cause(ctx) != context.DeadlineExceeded {
		t.Fatalf("cause = %v, want DeadlineExceeded", context.Cause(ctx))
	}
}

// Pausing the innermost budget must freeze the whole enclosing chain.
func TestBudget_NestedPausePropagates(t *testing.T) {
	parent, cancelP := deadline.WithBudget(context.Background(), 80*time.Millisecond)
	defer cancelP()
	child, cancelC := deadline.WithBudget(parent, 80*time.Millisecond)
	defer cancelC()

	deadline.PauserFrom(child).Pause()
	// Both budgets are well past 80ms but paused; neither may fire.
	if doneWithin(child, 250*time.Millisecond) {
		t.Fatal("child fired while chain paused")
	}
	if parent.Err() != nil {
		t.Fatal("parent fired while chain paused")
	}
	deadline.PauserFrom(child).Resume()
	if !doneWithin(child, 400*time.Millisecond) {
		t.Fatal("child did not resume")
	}
}

func TestBudget_ManualCancelHasCanceledCause(t *testing.T) {
	ctx, cancel := deadline.WithBudget(context.Background(), time.Hour)
	cancel()
	if !doneWithin(ctx, 100*time.Millisecond) {
		t.Fatal("manual cancel did not fire")
	}
	if context.Cause(ctx) != context.Canceled {
		t.Fatalf("cause = %v, want Canceled", context.Cause(ctx))
	}
}

func TestPauserFrom_NilWhenAbsent(t *testing.T) {
	if p := deadline.PauserFrom(context.Background()); p != nil {
		t.Fatalf("PauserFrom(Background) = %v, want nil", p)
	}
}

// An unbalanced Resume (no matching Pause) is a harmless no-op — it must not
// panic nor disturb the running timer.
func TestBudget_UnbalancedResumeIsNoop(t *testing.T) {
	ctx, cancel := deadline.WithBudget(context.Background(), 80*time.Millisecond)
	defer cancel()

	deadline.PauserFrom(ctx).Resume() // no preceding Pause
	if doneWithin(ctx, 20*time.Millisecond) {
		t.Fatal("unbalanced Resume disturbed the timer (fired early)")
	}
	if !doneWithin(ctx, 300*time.Millisecond) {
		t.Fatal("timer stopped firing after an unbalanced Resume")
	}
}
