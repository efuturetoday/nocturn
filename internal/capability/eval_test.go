package capability_test

import (
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
)

func TestEvaluate_TimeWindow(t *testing.T) {
	policy := capability.Policy{Rules: []capability.Rule{
		{Capability: "email", Effect: capability.Allow, Epoch: capability.Permanent, Window: capability.Daily(8, 0, 22, 0)},
	}}
	call := capability.Call{Capability: "email"}

	inside := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	outside := time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)

	if got := policy.Evaluate(call, capability.Env{Now: inside}); got != capability.Allow {
		t.Fatalf("inside window: got %v, want Allow", got)
	}
	if got := policy.Evaluate(call, capability.Env{Now: outside}); got != capability.Deny {
		t.Fatalf("outside window: got %v, want Deny", got)
	}
}

func TestEvaluate_WindowWrapsMidnight(t *testing.T) {
	policy := capability.Policy{Rules: []capability.Rule{
		{Capability: "backup", Effect: capability.Allow, Epoch: capability.Permanent, Window: capability.Daily(22, 0, 6, 0)},
	}}
	call := capability.Call{Capability: "backup"}

	at2am := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	at2pm := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC)

	if got := policy.Evaluate(call, capability.Env{Now: at2am}); got != capability.Allow {
		t.Fatalf("02:00 within overnight window: got %v, want Allow", got)
	}
	if got := policy.Evaluate(call, capability.Env{Now: at2pm}); got != capability.Deny {
		t.Fatalf("14:00 outside overnight window: got %v, want Deny", got)
	}
}

func TestEvaluate_RateLimit(t *testing.T) {
	now := time.Unix(1000, 0)
	rl := capability.NewRateLimiter(2, time.Minute, capability.WithClock(func() time.Time { return now }))
	env := capability.Env{RateAllow: func(c capability.Call) bool { return rl.Allow(c.Capability) }}

	policy := capability.Policy{Rules: []capability.Rule{
		{Capability: "email", Effect: capability.Allow, Epoch: capability.Permanent},
	}}
	call := capability.Call{Capability: "email"}

	if policy.Evaluate(call, env) != capability.Allow {
		t.Fatal("1st call within budget must be Allow")
	}
	if policy.Evaluate(call, env) != capability.Allow {
		t.Fatal("2nd call within budget must be Allow")
	}
	if policy.Evaluate(call, env) != capability.Deny {
		t.Fatal("3rd call over budget must be Deny")
	}
}

// Match conditions and the rate check compose: outside the window the rule does
// not even match (Deny) and no rate budget is consumed.
func TestEvaluate_WindowAndRateCompose(t *testing.T) {
	now := time.Unix(1000, 0)
	rl := capability.NewRateLimiter(1, time.Minute, capability.WithClock(func() time.Time { return now }))
	rateCalls := 0
	env := capability.Env{
		Now: time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC), // outside window
		RateAllow: func(c capability.Call) bool {
			rateCalls++
			return rl.Allow(c.Capability)
		},
	}
	policy := capability.Policy{Rules: []capability.Rule{
		{Capability: "email", Effect: capability.Allow, Epoch: capability.Permanent, Window: capability.Daily(8, 0, 22, 0)},
	}}

	if got := policy.Evaluate(capability.Call{Capability: "email"}, env); got != capability.Deny {
		t.Fatalf("outside window: got %v, want Deny", got)
	}
	if rateCalls != 0 {
		t.Fatal("rate budget must not be consumed when the rule does not match")
	}
}
