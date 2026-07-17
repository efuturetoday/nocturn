package capability_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// allow is a tiny helper: it drops the retry-after when a test only cares about the
// allowed bool.
func allow(rl *capability.RateLimiter, key string) bool {
	ok, _ := rl.Allow(key)
	return ok
}

func TestRateLimiter_AllowsUpToLimitThenDenies(t *testing.T) {
	rl := capability.NewRateLimiter(capability.WithLimit("email", 3, time.Minute))
	for i := 1; i <= 3; i++ {
		if !allow(rl, "email") {
			t.Fatalf("call %d within limit must be allowed", i)
		}
	}
	if allow(rl, "email") {
		t.Fatal("4th call within the window must be denied")
	}
}

// The sliding window is tested under testing/synctest: time.Sleep advances the bubble's
// fake clock instantly, so no injected clock is needed (go.dev/blog/testing-time).
func TestRateLimiter_WindowSlides(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl := capability.NewRateLimiter(capability.WithLimit("k", 1, time.Minute))
		if !allow(rl, "k") {
			t.Fatal("first call allowed")
		}
		if allow(rl, "k") {
			t.Fatal("second call within window denied")
		}
		time.Sleep(61 * time.Second) // past the window
		if !allow(rl, "k") {
			t.Fatal("after the window slides, a call is allowed again")
		}
	})
}

// A denied call is not recorded, so it does not extend the window.
func TestRateLimiter_DeniedCallDoesNotExtendWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl := capability.NewRateLimiter(capability.WithLimit("k", 1, time.Minute))
		allow(rl, "k") // t0, recorded
		time.Sleep(30 * time.Second)
		if allow(rl, "k") { // t0+30s, over limit -> denied, NOT recorded
			t.Fatal("second call within window must be denied")
		}
		time.Sleep(31 * time.Second) // t0+61s: past the only recorded call
		if !allow(rl, "k") {
			t.Fatal("the recorded call has aged out; a new call must be allowed")
		}
	})
}

func TestRateLimiter_KeysAreIndependent(t *testing.T) {
	rl := capability.NewRateLimiter(
		capability.WithLimit("a", 1, time.Minute),
		capability.WithLimit("b", 1, time.Minute),
	)
	if !allow(rl, "a") || !allow(rl, "b") {
		t.Fatal("distinct keys have independent budgets")
	}
	if allow(rl, "a") {
		t.Fatal("key a is over its own limit")
	}
}

// A key with no configured limit is unlimited — so rating "notify" never rate-limits
// bursty reads (http.read, file.read) that were never configured.
func TestRateLimiter_UnconfiguredKeyIsUnlimited(t *testing.T) {
	rl := capability.NewRateLimiter(capability.WithLimit("notify", 1, time.Minute))
	for range 1000 {
		if !allow(rl, "http.read") {
			t.Fatal("unconfigured key must always be allowed")
		}
	}
	if !allow(rl, "notify") || allow(rl, "notify") {
		t.Fatal("configured key must still enforce its limit")
	}
}

// A denied call reports how long until a slot frees, so the caller can tell the model
// when to retry. With a 1/min limit, the second call's retry-after is ~the window.
func TestRateLimiter_ReportsRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl := capability.NewRateLimiter(capability.WithLimit("notify", 1, time.Minute))
		if ok, _ := rl.Allow("notify"); !ok {
			t.Fatal("first call allowed")
		}
		time.Sleep(20 * time.Second)
		ok, retry := rl.Allow("notify")
		if ok {
			t.Fatal("second call within window must be denied")
		}
		// The recorded call ages out one window after it was made → 60s - 20s elapsed = 40s.
		if retry != 40*time.Second {
			t.Fatalf("retryAfter = %s, want 40s (window minus elapsed)", retry)
		}
	})
}
