package agentkit

import (
	"context"
	"testing"
	"time"
)

// A span parked in Pause must be subtracted from a call's active duration, so a tool that waited on
// an out-of-band approval reports its execution time, not the human's decision time.
func TestActiveSince_ExcludesPausedSpan(t *testing.T) {
	ctx := withPausedClock(context.Background())

	start := time.Now()
	pausedStart := pausedNanos(ctx)

	// Simulate a real call: a little work, then a parked approval wait, then a little more work.
	time.Sleep(5 * time.Millisecond)
	resume := Pause(ctx)
	time.Sleep(40 * time.Millisecond) // human deciding — must not count
	resume()
	time.Sleep(5 * time.Millisecond)

	active := activeSince(ctx, start, pausedStart)
	wall := time.Since(start)

	if active >= wall {
		t.Fatalf("active %v should be less than wall %v", active, wall)
	}
	// Active is the ~10ms of work, not the ~50ms wall; allow generous slack for scheduler jitter.
	if active > 30*time.Millisecond {
		t.Fatalf("active %v still includes the paused span (~40ms)", active)
	}
}

// A nested pause is banked on the SAME shared clock, so a parent call's window also excludes it.
func TestPausedClock_SharedAcrossNesting(t *testing.T) {
	ctx := withPausedClock(context.Background())
	if got := pausedNanos(ctx); got != 0 {
		t.Fatalf("fresh clock = %d, want 0", got)
	}
	resume := Pause(ctx)
	time.Sleep(20 * time.Millisecond)
	resume()
	if got := pausedNanos(ctx); got < int64(15*time.Millisecond) {
		t.Fatalf("banked %v, want >= ~20ms", time.Duration(got))
	}
}

// Pause with no clock and no deadlines installed is a safe no-op.
func TestPause_NoClock_NoOp(t *testing.T) {
	resume := Pause(context.Background())
	resume() // must not panic
	if got := pausedNanos(context.Background()); got != 0 {
		t.Fatalf("no clock installed = %d, want 0", got)
	}
}
