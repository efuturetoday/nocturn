package capability_test

import (
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
)

func TestRateLimiter_AllowsUpToLimitThenDenies(t *testing.T) {
	now := time.Unix(1000, 0)
	rl := capability.NewRateLimiter(3, time.Minute, capability.WithClock(func() time.Time { return now }))

	for i := 1; i <= 3; i++ {
		if !rl.Allow("email") {
			t.Fatalf("call %d within limit must be allowed", i)
		}
	}
	if rl.Allow("email") {
		t.Fatal("4th call within the window must be denied")
	}
}

func TestRateLimiter_WindowSlides(t *testing.T) {
	now := time.Unix(1000, 0)
	rl := capability.NewRateLimiter(1, time.Minute, capability.WithClock(func() time.Time { return now }))

	if !rl.Allow("k") {
		t.Fatal("first call allowed")
	}
	if rl.Allow("k") {
		t.Fatal("second call within window denied")
	}

	now = now.Add(61 * time.Second) // move past the window
	if !rl.Allow("k") {
		t.Fatal("after the window slides, a call is allowed again")
	}
}

// A denied call is not recorded, so it does not extend the window.
func TestRateLimiter_DeniedCallDoesNotExtendWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	rl := capability.NewRateLimiter(1, time.Minute, capability.WithClock(func() time.Time { return now }))

	rl.Allow("k") // t=1000, recorded
	now = now.Add(30 * time.Second)
	if rl.Allow("k") { // t=1030, over limit -> denied, NOT recorded
		t.Fatal("second call within window must be denied")
	}
	now = now.Add(31 * time.Second) // t=1061: 61s after the only recorded call
	if !rl.Allow("k") {
		t.Fatal("the recorded call has aged out; a new call must be allowed")
	}
}

func TestRateLimiter_KeysAreIndependent(t *testing.T) {
	now := time.Unix(1000, 0)
	rl := capability.NewRateLimiter(1, time.Minute, capability.WithClock(func() time.Time { return now }))

	if !rl.Allow("a") || !rl.Allow("b") {
		t.Fatal("distinct keys have independent budgets")
	}
	if rl.Allow("a") {
		t.Fatal("key a is over its own limit")
	}
}
