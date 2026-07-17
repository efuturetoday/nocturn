package capability_test

import (
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
)

func TestPolicy_Evaluate(t *testing.T) {
	cap := func(name string, host string) capability.Call {
		c := capability.Call{Family: name}
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
			policy: capability.Policy{Rules: []capability.Rule{{Family: "log", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("log", ""),
			want:   capability.Allow,
		},
		{
			name: "deny wins over allow for same family",
			policy: capability.Policy{Rules: []capability.Rule{
				{Family: "log", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent},
				{Family: "log", Writes: capability.MatchAny, Effect: capability.Deny, Epoch: capability.Permanent},
			}},
			call: cap("log", ""),
			want: capability.Deny,
		},
		{
			name: "ask beats allow but loses to deny",
			policy: capability.Policy{Rules: []capability.Rule{
				{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent},
				{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Ask, Epoch: capability.Permanent},
			}},
			call: cap("http", "api.example.com"),
			want: capability.Ask,
		},
		{
			name:   "explicit star family matches any",
			policy: capability.Policy{Rules: []capability.Rule{{Family: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("log", ""),
			want:   capability.Allow,
		},
		{
			name:   "empty family matches nothing (fail closed)",
			policy: capability.Policy{Rules: []capability.Rule{{Family: "", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("log", ""),
			want:   capability.Deny,
		},
		{
			name:   "MatchNone (forgotten Writes) matches nothing (fail closed)",
			policy: capability.Policy{Rules: []capability.Rule{{Family: "log", Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("log", ""),
			want:   capability.Deny,
		},
		{
			name:   "forgotten host does NOT allow a host-bearing call",
			policy: capability.Policy{Rules: []capability.Rule{{Family: "http", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("http", "evil.com"),
			want:   capability.Deny,
		},
		{
			name:   "explicit star host allows any host",
			policy: capability.Policy{Rules: []capability.Rule{{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("http", "anything.com"),
			want:   capability.Allow,
		},
		{
			name:   "host rule does not match a hostless call",
			policy: capability.Policy{Rules: []capability.Rule{{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("http", ""),
			want:   capability.Deny,
		},
		{
			name:   "family mismatch falls through to default deny",
			policy: capability.Policy{Rules: []capability.Rule{{Family: "log", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("http", ""),
			want:   capability.Deny,
		},
		{
			name:   "host glob allows matching host",
			policy: capability.Policy{Rules: []capability.Rule{{Family: "http", TargetGlob: "*.example.com", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("http", "api.example.com"),
			want:   capability.Allow,
		},
		{
			name:   "host glob denies non-matching host by default",
			policy: capability.Policy{Rules: []capability.Rule{{Family: "http", TargetGlob: "*.example.com", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("http", "evil.com"),
			want:   capability.Deny,
		},
		{
			name:   "read-only rule does NOT match a write",
			policy: capability.Policy{Rules: []capability.Rule{{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   capability.Call{Family: "http", Write: true, Target: "api.example.com"},
			want:   capability.Deny,
		},
		{
			name:   "write-only rule does NOT match a read",
			policy: capability.Policy{Rules: []capability.Rule{{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchWrite, Effect: capability.Ask, Epoch: capability.Permanent}}},
			call:   capability.Call{Family: "http", Write: false, Target: "api.example.com"},
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

// The base "reads still, writes ask" policy: one MatchRead→Allow rule and one
// MatchWrite→Ask rule cover every family at once, keyed only on the mutation axis.
func TestPolicy_ReadsAllowWritesAsk(t *testing.T) {
	policy := capability.Policy{Rules: []capability.Rule{
		{Family: capability.Wildcard, TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
		{Family: capability.Wildcard, TargetGlob: capability.Wildcard, Writes: capability.MatchWrite, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
	read := capability.Call{Family: "http", Write: false, Target: "api.example.com"}
	write := capability.Call{Family: "http", Write: true, Target: "api.example.com"}

	if got := policy.Evaluate(read, capability.Env{}); got != capability.Allow {
		t.Fatalf("read: got %v, want Allow (reads run still)", got)
	}
	if got := policy.Evaluate(write, capability.Env{}); got != capability.Ask {
		t.Fatalf("write: got %v, want Ask", got)
	}
}

func TestEvaluate_TimeWindow(t *testing.T) {
	policy := capability.Policy{Rules: []capability.Rule{
		{Family: "email", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent, Window: capability.Daily(8, 0, 22, 0)},
	}}
	call := capability.Call{Family: "email"}

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
		{Family: "backup", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent, Window: capability.Daily(22, 0, 6, 0)},
	}}
	call := capability.Call{Family: "backup"}

	at2am := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	at2pm := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC)

	if got := policy.Evaluate(call, capability.Env{Now: at2am}); got != capability.Allow {
		t.Fatalf("02:00 within overnight window: got %v, want Allow", got)
	}
	if got := policy.Evaluate(call, capability.Env{Now: at2pm}); got != capability.Deny {
		t.Fatalf("14:00 outside overnight window: got %v, want Deny", got)
	}
}

// A windowed rule does not match outside its window, so the call falls through to
// deny-by-default. (Rate limiting is the gateway's concern now, not Evaluate's — see
// gateway.Guard.rateCheck and the gateway authorize tests.)
func TestEvaluate_OutsideWindowDenies(t *testing.T) {
	env := capability.Env{Now: time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)} // outside window
	policy := capability.Policy{Rules: []capability.Rule{
		{Family: "email", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent, Window: capability.Daily(8, 0, 22, 0)},
	}}

	if got := policy.Evaluate(capability.Call{Family: "email"}, env); got != capability.Deny {
		t.Fatalf("outside window: got %v, want Deny", got)
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
			{Family: "file", TargetGlob: glob, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent},
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
		got := rule(tc.glob).Evaluate(capability.Call{Family: "file", Write: true, Target: tc.target}, capability.Env{})
		if got != tc.want {
			t.Errorf("glob %q target %q: got %v, want %v", tc.glob, tc.target, got, tc.want)
		}
	}
}

// The reach limiter understands IP ranges (CIDR) and numeric IP equality for
// host-family targets, so a cage/policy can bound http/dns/ping to a subnet — not
// only to a host glob. A CIDR/IP glob matches only an IP-literal target; a hostname
// is never resolved in the decision layer.
func TestEvaluate_TargetIPRange(t *testing.T) {
	rule := func(glob string) capability.Policy {
		return capability.Policy{Rules: []capability.Rule{
			{Family: "http", TargetGlob: glob, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent},
		}}
	}
	cases := []struct {
		glob, target string
		want         capability.Decision
	}{
		// CIDR containment (v4)
		{"10.0.0.0/8", "10.1.2.3", capability.Allow},
		{"10.0.0.0/8", "11.0.0.1", capability.Deny},
		{"192.168.0.0/16", "192.168.1.20", capability.Allow},
		{"192.168.0.0/16", "192.169.1.20", capability.Deny},
		// CIDR containment (v6)
		{"2001:db8::/32", "2001:db8::1", capability.Allow},
		{"2001:db8::/32", "2001:dead::1", capability.Deny},
		// a CIDR never matches a hostname target (no resolution in the broker)
		{"10.0.0.0/8", "example.com", capability.Deny},
		// bare-IP glob is numeric: an alternate IPv6 spelling of the same address matches
		{"2001:db8::1", "2001:0db8:0000:0000:0000:0000:0000:0001", capability.Allow},
		{"10.0.0.5", "10.0.0.5", capability.Allow},
		{"10.0.0.5", "10.0.0.6", capability.Deny},
		// a glob is still a glob for hostnames — unchanged
		{"*.example.com", "api.example.com", capability.Allow},
		{"*.example.com", "example.org", capability.Deny},
		// a prefix glob over an IP still works (single-segment "*")
		{"192.168.1.*", "192.168.1.55", capability.Allow},
	}
	for _, tc := range cases {
		got := rule(tc.glob).Evaluate(capability.Call{Family: "http", Write: false, Target: tc.target}, capability.Env{})
		if got != tc.want {
			t.Errorf("glob %q target %q: got %v, want %v", tc.glob, tc.target, got, tc.want)
		}
	}
}
