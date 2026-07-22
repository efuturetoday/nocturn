package agentkit

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// Same-package coverage for the unexported guard machinery: withTimeout / installDeadline / spend /
// enterSpawn and the pausable deadline internals. (The public Pause / WithPausableBudget surface is
// tested externally in guards_test.go; the paused-clock accounting in guards_paused_test.go.)

func setLen(set *pausables) int {
	set.mu.Lock()
	defer set.mu.Unlock()
	return len(set.items)
}

func TestWithTimeout_DeadlineFires_CancelsWithErrTurnTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := withTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		time.Sleep(51 * time.Millisecond)
		synctest.Wait()
		if !errors.Is(context.Cause(ctx), ErrTurnTimeout) {
			t.Fatalf("cause = %v, want ErrTurnTimeout", context.Cause(ctx))
		}
	})
}

func TestWithTimeout_InheritIfPresent(t *testing.T) {
	ctx1, c1 := withTimeout(context.Background(), 50*time.Millisecond)
	defer c1()
	set := ctx1.Value(pausablesKey{}).(*pausables)
	if n := setLen(set); n != 1 {
		t.Fatalf("deadline set len = %d, want 1", n)
	}
	// A second withTimeout on a ctx that already carries a set inherits it: plain WithCancel, NO new
	// deadline — so an embedded run reuses the parent's remaining time.
	ctx2, c2 := withTimeout(ctx1, 50*time.Millisecond)
	defer c2()
	if ctx2.Value(pausablesKey{}) != set {
		t.Fatal("inherited ctx carries a different deadline set")
	}
	if n := setLen(set); n != 1 {
		t.Fatalf("deadline set len = %d after inherit, want still 1", n)
	}
}

func TestWithTimeout_ZeroDuration_NoDeadline(t *testing.T) {
	ctx, cancel := withTimeout(context.Background(), 0)
	defer cancel()
	if ctx.Value(pausablesKey{}) != nil {
		t.Fatal("zero duration installed a deadline, want none")
	}
}

func TestWithPausableBudget_AddsToSet_NotInherited(t *testing.T) {
	ctx1, c1 := WithPausableBudget(context.Background(), 50*time.Millisecond)
	defer c1()
	set := ctx1.Value(pausablesKey{}).(*pausables)
	if n := setLen(set); n != 1 {
		t.Fatalf("set len = %d, want 1", n)
	}
	// Unlike the turn deadline, a nested budget always ADDS its own to the set.
	ctx2, c2 := WithPausableBudget(ctx1, 50*time.Millisecond)
	defer c2()
	if ctx2.Value(pausablesKey{}) != set {
		t.Fatal("second budget landed in a different set")
	}
	if n := setLen(set); n != 2 {
		t.Fatalf("set len = %d after second budget, want 2", n)
	}
}

func TestWithTokenBudget_InheritIfPresent(t *testing.T) {
	ctx1 := withTokenBudget(context.Background(), 100)
	b1 := ctx1.Value(tokenBudgetKey{}).(*tokenBudget)
	ctx2 := withTokenBudget(ctx1, 999)
	b2 := ctx2.Value(tokenBudgetKey{}).(*tokenBudget)
	if b1 != b2 {
		t.Fatal("second withTokenBudget started a fresh pool, want the parent's")
	}
	if b2.limit != 100 {
		t.Fatalf("inherited limit = %d, want the parent's 100 (second arg ignored)", b2.limit)
	}
}

func TestSpend_OverLimit(t *testing.T) {
	ctx := withTokenBudget(context.Background(), 100)
	if spend(ctx, 60) {
		t.Fatal("spend(60) reported over, want under")
	}
	if !spend(ctx, 40) {
		t.Fatal("spend to 100 reported under, want over (>= limit)")
	}
}

func TestSpend_NoPoolOrNoLimit_NeverOver(t *testing.T) {
	if spend(context.Background(), 1_000_000) {
		t.Fatal("spend with no pool reported over, want never over")
	}
	ctx := withTokenBudget(context.Background(), 0) // 0 = unlimited
	if spend(ctx, 1_000_000) {
		t.Fatal("spend with unlimited pool reported over, want never over")
	}
}

func TestSpend_SharedAcrossNesting(t *testing.T) {
	ctx := withTokenBudget(context.Background(), 100)
	ctx2 := withTokenBudget(ctx, 50) // inherits the same pool
	if spend(ctx, 60) {
		t.Fatal("first spend reported over")
	}
	if !spend(ctx2, 60) {
		t.Fatal("nested spend did not draw the shared pool (120 >= 100)")
	}
}

func TestEnterSpawn_MaxDepth(t *testing.T) {
	ctx := withSpawnLimits(context.Background(), 2, 64)
	c1, err := enterSpawn(ctx)
	if err != nil {
		t.Fatalf("depth 1: %v", err)
	}
	c2, err := enterSpawn(c1)
	if err != nil {
		t.Fatalf("depth 2: %v", err)
	}
	if _, err := enterSpawn(c2); !errors.Is(err, ErrMaxDepth) {
		t.Fatalf("depth 3 err = %v, want ErrMaxDepth", err)
	}
}

func TestEnterSpawn_MaxSpawns(t *testing.T) {
	ctx := withSpawnLimits(context.Background(), 10, 2)
	if _, err := enterSpawn(ctx); err != nil {
		t.Fatalf("spawn 1: %v", err)
	}
	if _, err := enterSpawn(ctx); err != nil {
		t.Fatalf("spawn 2: %v", err)
	}
	if _, err := enterSpawn(ctx); !errors.Is(err, ErrMaxSpawns) {
		t.Fatalf("spawn 3 err = %v, want ErrMaxSpawns", err)
	}
}

func TestEnterSpawn_NoState_NoOp(t *testing.T) {
	ctx, err := enterSpawn(context.Background())
	if err != nil || ctx != context.Background() {
		t.Fatalf("enterSpawn(bg) = (%v, %v), want (bg, nil)", ctx, err)
	}
}

func TestWithSpawnLimits_Defaults(t *testing.T) {
	ctx := withSpawnLimits(context.Background(), 0, 0)
	st := ctx.Value(spawnKey{}).(*spawnState)
	if st.maxDepth != defaultMaxDepth || st.maxSpawns != defaultMaxSpawns {
		t.Fatalf("defaults = {%d %d}, want {%d %d}", st.maxDepth, st.maxSpawns, defaultMaxDepth, defaultMaxSpawns)
	}
}

func TestEnterSpawn_DepthPerBranch_PopulationShared(t *testing.T) {
	// Depth is per-branch (each branch counts fresh from the shared ctx); the total-spawn population
	// is a shared counter. With maxSpawns=4, the 5th spawn anywhere trips ErrMaxSpawns.
	ctx := withSpawnLimits(context.Background(), 10, 4)
	a1, _ := enterSpawn(ctx) // spawn 1, depth 1
	a2, _ := enterSpawn(a1)  // spawn 2, depth 2
	b1, _ := enterSpawn(ctx) // spawn 3, depth 1 again (branch depth reset)
	b2, _ := enterSpawn(b1)  // spawn 4, depth 2
	_ = a2
	_ = b2
	if _, err := enterSpawn(ctx); !errors.Is(err, ErrMaxSpawns) {
		t.Fatalf("5th spawn err = %v, want ErrMaxSpawns (population shared)", err)
	}
}

func TestInstallDeadline_StopFunc_RemovesFromSet(t *testing.T) {
	ctx, stop := installDeadline(context.Background(), time.Hour)
	set := ctx.Value(pausablesKey{}).(*pausables)
	if n := setLen(set); n != 1 {
		t.Fatalf("set len = %d, want 1", n)
	}
	stop()
	if n := setLen(set); n != 0 {
		t.Fatalf("set len = %d after stop, want 0", n)
	}
	// The stop func cancels with context.Canceled, NOT ErrTurnTimeout (that's only a fired deadline).
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatalf("cause after stop = %v, want context.Canceled", context.Cause(ctx))
	}
}

func TestPause_Idempotent_DoublePause(t *testing.T) {
	ctx, stop := installDeadline(context.Background(), time.Hour)
	defer stop()
	set := ctx.Value(pausablesKey{}).(*pausables)
	set.mu.Lock()
	dl := set.items[0]
	set.mu.Unlock()

	r1 := dl.pause()
	r2 := dl.pause() // second pause is a no-op
	r1()
	r2()

	dl.mu.Lock()
	paused := dl.paused
	dl.mu.Unlock()
	if paused {
		t.Fatal("deadline still paused after resume, want running")
	}
}

func TestPause_AfterFired_RemainingZero(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := installDeadline(context.Background(), 10*time.Millisecond)
		defer stop()
		set := ctx.Value(pausablesKey{}).(*pausables)
		set.mu.Lock()
		dl := set.items[0]
		set.mu.Unlock()

		time.Sleep(11 * time.Millisecond) // let it fire
		synctest.Wait()

		resume := dl.pause() // pausing an already-fired deadline banks zero remaining
		resume()
		dl.mu.Lock()
		rem := dl.remaining
		dl.mu.Unlock()
		if rem != 0 {
			t.Fatalf("remaining after fire = %v, want 0", rem)
		}
	})
}

func TestActiveSince_NeverNegative(t *testing.T) {
	// pausedStart greater than the ctx's current banked nanos would make the raw delta negative;
	// activeSince clamps to zero.
	if got := activeSince(context.Background(), time.Now(), -int64(time.Hour)); got != 0 {
		t.Fatalf("activeSince = %v, want 0 (never negative)", got)
	}
}
