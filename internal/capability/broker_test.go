package capability_test

import (
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
)

func TestPolicy_Evaluate(t *testing.T) {
	cap := func(name string, host string) capability.Call {
		c := capability.Call{Capability: name}
		if host != "" {
			c.Attrs = map[string]string{"host": host}
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
				{Capability: "net.fetch", HostGlob: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent},
				{Capability: "net.fetch", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
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
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "net.fetch", HostGlob: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("net.fetch", "anything.com"),
			want:   capability.Allow,
		},
		{
			name:   "host rule does not match a hostless call",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "net.fetch", HostGlob: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent}}},
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
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "net.fetch", HostGlob: "*.example.com", Effect: capability.Allow, Epoch: capability.Permanent}}},
			call:   cap("net.fetch", "api.example.com"),
			want:   capability.Allow,
		},
		{
			name:   "host glob denies non-matching host by default",
			policy: capability.Policy{Rules: []capability.Rule{{Capability: "net.fetch", HostGlob: "*.example.com", Effect: capability.Allow, Epoch: capability.Permanent}}},
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
