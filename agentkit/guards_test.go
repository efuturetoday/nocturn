package agentkit_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
)

// The public guard surface (Pause, WithPausableBudget) — the unexported machinery
// (installDeadline/withTimeout/spend/enterSpawn) is covered same-package in guards_internal_test.go.

func TestPause_BanksPausedTime_DeadlineNotTripped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := agentkit.WithPausableBudget(context.Background(), 100*time.Millisecond)
		defer stop()

		resume := agentkit.Pause(ctx)
		time.Sleep(500 * time.Millisecond) // parked far past the deadline — must NOT trip while paused
		synctest.Wait()
		if err := ctx.Err(); err != nil {
			t.Fatalf("deadline tripped while paused: %v", err)
		}
		resume()

		// After resume the full budget remains: still alive just before it, tripped just after.
		time.Sleep(99 * time.Millisecond)
		synctest.Wait()
		if err := ctx.Err(); err != nil {
			t.Fatalf("tripped early after resume: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
		synctest.Wait()
		if !errors.Is(context.Cause(ctx), agentkit.ErrTurnTimeout) {
			t.Fatalf("cause = %v, want ErrTurnTimeout after active time elapsed", context.Cause(ctx))
		}
	})
}

func TestWithPausableBudget_PausedByPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := agentkit.WithPausableBudget(context.Background(), 100*time.Millisecond)
		defer stop()

		resume := agentkit.Pause(ctx)
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()
		if err := ctx.Err(); err != nil {
			t.Fatalf("nested budget fired during pause: %v", err)
		}
		resume()
	})
}

func TestWithPausableBudget_FiresWhenActiveExceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := agentkit.WithPausableBudget(context.Background(), 50*time.Millisecond)
		defer stop()

		time.Sleep(51 * time.Millisecond)
		synctest.Wait()
		if !errors.Is(context.Cause(ctx), agentkit.ErrTurnTimeout) {
			t.Fatalf("cause = %v, want ErrTurnTimeout", context.Cause(ctx))
		}
	})
}
