package capability_test

import (
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
)

func TestPolicy_Evaluate(t *testing.T) {
	cap := func(name string, host string) capability.Call {
		c := capability.Call{Capability: name}
		if host != "" {
			c.Target = host
		}
		return c
	}

	tests := []struct {
		name   string
		policy capability.Policy
		call   capability.Call
		want   capability.Decision
	}{
		{
			name:   "empty policy denies by default",
			policy: capability.Policy{},
			call:   cap("log", ""),
			want:   capability.Deny,
		},
		{
			name:   "matching allow permits",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "log", Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("log", ""),
			want:   capability.Allow,
		},
		{
			name: "deny wins over allow for same capability",
			policy: capability.Policy{Rules: []capability.Rule{
				{Capability: "log", Effect: capability.Allow, Epoch: capability.Permanent},
				{Capability: "log", Effect: capability.Deny, Epoch: capability.Permanent},
			}},
			call: cap("log", ""),
			want: capability.Deny,
		},
		{
			name: "ask beats allow but loses to deny",
			policy: capability.Policy{Rules: []capability.Rule{
				{Capability: "net.fetch", TargetGlob: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent},
				{Capability: "net.fetch", TargetGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
			}},
			call: cap("net.fetch", "api.example.com"),
			want: capability.Ask,
		},
		{
			name:   "explicit star capability matches any",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("log", ""),
			want:   capability.Allow,
		},
		{
			name:   "empty capability matches nothing (fail closed)",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "", Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("log", ""),
			want:   capability.Deny,
		},
		{
			name:   "forgotten host does NOT allow a host-bearing call",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "net.fetch", Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("net.fetch", "evil.com"),
			want:   capability.Deny,
		},
		{
			name:   "explicit star host allows any host",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "net.fetch", TargetGlob: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("net.fetch", "anything.com"),
			want:   capability.Allow,
		},
		{
			name:   "host rule does not match a hostless call",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "net.fetch", TargetGlob: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("net.fetch", ""),
			want:   capability.Deny,
		},
		{
			name:   "capability mismatch falls through to default deny",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "log", Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("net.fetch", ""),
			want:   capability.Deny,
		},
		{
			name:   "host glob allows matching host",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "net.fetch", TargetGlob: "*.example.com", Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("net.fetch", "api.example.com"),
			want:   capability.Allow,
		},
		{
			name:   "host glob denies non-matching host by default",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "net.fetch", TargetGlob: "*.example.com", Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("net.fetch", "evil.com"),
			want:   capability.Deny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.Evaluate(tt.call, capability.Env{}); got != tt.want {
				t.Fatalf("Evaluate(%+v) = %v, want %v", tt.call, got, tt.want)
			}
		})
	}
}

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

// Target semantics: an exact glob is path.Match (so "*" does not cross "/"), but
// the explicit Wildcard token matches ANY target — including a multi-segment path
// like "notes/todo.md". Without the Wildcard special-case, a "*" rule would fail
// to match paths, silently denying every file.* call. A targetless call never
// matches a target-scoped rule (even Wildcard).
func TestEvaluate_TargetGlobAndWildcard(t *testing.T) {
	rule := func(glob string) capability.Policy {
		return capability.Policy{Rules: []capability.Rule{
			{Capability: "file.write", TargetGlob: glob, Effect: capability.Allow, Epoch: capability.Permanent},
		}}
	}
	cases := []struct {
		glob, target string
		want         capability.Decision
	}{
		{"*", "notes/todo.md", capability.Allow}, // Wildcard crosses "/"
		{"*", "flat", capability.Allow},          // and matches a flat target
		{"*", "", capability.Deny},               // but a target must be present
		{"notes/*", "notes/todo.md", capability.Allow},
		{"notes/*", "notes/deep/x.md", capability.Deny}, // path.Match "*" stops at "/"
		{"notes/*", "secrets/k", capability.Deny},
	}
	for _, tc := range cases {
		got := rule(tc.glob).Evaluate(capability.Call{Capability: "file.write", Target: tc.target}, capability.Env{})
		if got != tc.want {
			t.Errorf("glob %q target %q: got %v, want %v", tc.glob, tc.target, got, tc.want)
		}
	}
}
