package deadline_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/internal/deadline"
)

// doneWithin reports whether ctx is done within d. Inside a synctest bubble both the
// budget's timer and this time.After run on the fake clock, so the race resolves
// deterministically and instantly (go.dev/blog/testing-time).
func doneWithin(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func TestBudget_FiresAfterDuration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := deadline.WithBudget(context.Background(), time.Second)
		defer cancel()

		if doneWithin(ctx, 500*time.Millisecond) {
			t.Fatal("budget fired too early")
		}
		if !doneWithin(ctx, 5*time.Second) {
			t.Fatal("budget never fired")
		}
		if context.Cause(ctx) != context.DeadlineExceeded {
			t.Fatalf("cause = %v, want DeadlineExceeded", context.Cause(ctx))
		}
	})
}

func TestBudget_PauseFreezesClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := deadline.WithBudget(context.Background(), time.Second)
		defer cancel()

		p := deadline.PauserFrom(ctx)
		if p == nil {
			t.Fatal("PauserFrom returned nil for a budget context")
		}
		p.Pause()
		// Well past the 1s budget — but paused, so it must not fire.
		if doneWithin(ctx, time.Hour) {
			t.Fatal("budget fired while paused")
		}
		p.Resume()
		if !doneWithin(ctx, 5*time.Second) {
			t.Fatal("budget did not resume after Resume")
		}
		if context.Cause(ctx) != context.DeadlineExceeded {
			t.Fatalf("cause = %v, want DeadlineExceeded", context.Cause(ctx))
		}
	})
}

// Pausing the innermost budget must freeze the whole enclosing chain.
func TestBudget_NestedPausePropagates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancelP := deadline.WithBudget(context.Background(), time.Second)
		defer cancelP()
		child, cancelC := deadline.WithBudget(parent, time.Second)
		defer cancelC()

		deadline.PauserFrom(child).Pause()
		// Both budgets are well past 1s but paused; neither may fire.
		if doneWithin(child, time.Hour) {
			t.Fatal("child fired while chain paused")
		}
		if parent.Err() != nil {
			t.Fatal("parent fired while chain paused")
		}
		deadline.PauserFrom(child).Resume()
		if !doneWithin(child, 5*time.Second) {
			t.Fatal("child did not resume")
		}
	})
}

func TestBudget_ManualCancelHasCanceledCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := deadline.WithBudget(context.Background(), time.Hour)
		cancel()
		if !doneWithin(ctx, time.Second) {
			t.Fatal("manual cancel did not fire")
		}
		if context.Cause(ctx) != context.Canceled {
			t.Fatalf("cause = %v, want Canceled", context.Cause(ctx))
		}
	})
}

func TestPauserFrom_NilWhenAbsent(t *testing.T) {
	if p := deadline.PauserFrom(context.Background()); p != nil {
		t.Fatalf("PauserFrom(Background) = %v, want nil", p)
	}
}

// An unbalanced Resume (no matching Pause) is a harmless no-op — it must not
// panic nor disturb the running timer.
func TestBudget_UnbalancedResumeIsNoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := deadline.WithBudget(context.Background(), time.Second)
		defer cancel()

		deadline.PauserFrom(ctx).Resume() // no preceding Pause
		if doneWithin(ctx, 500*time.Millisecond) {
			t.Fatal("unbalanced Resume disturbed the timer (fired early)")
		}
		if !doneWithin(ctx, 5*time.Second) {
			t.Fatal("timer stopped firing after an unbalanced Resume")
		}
	})
}
