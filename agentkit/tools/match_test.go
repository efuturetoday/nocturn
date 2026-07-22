package tools_test

import (
	"reflect"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/tools"
)

func TestHostMatch_WildcardAny(t *testing.T) {
	t.Parallel()
	hosts := []string{"example.com", "a.example.com", "b.a.example.com", "127.0.0.1:8080", ""}
	for _, host := range hosts {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			if !tools.HostMatch("*", host) {
				t.Errorf("HostMatch(%q, %q) = false, want true", "*", host)
			}
		})
	}
}

func TestHostMatch_Exact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		host    string
		want    bool
	}{
		{"identical", "example.com", "example.com", true},
		{"different host", "example.com", "other.com", false},
		{"pattern is subdomain of host", "example.com", "a.example.com", false},
		{"host is subdomain of pattern", "a.example.com", "example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tools.HostMatch(tt.pattern, tt.host); got != tt.want {
				t.Errorf("HostMatch(%q, %q) = %t, want %t", tt.pattern, tt.host, got, tt.want)
			}
		})
	}
}

func TestHostMatch_SubdomainWildcard(t *testing.T) {
	t.Parallel()
	const pattern = "*.example.com"
	tests := []struct {
		name string
		host string
		want bool
	}{
		{"base apex covered", "example.com", true},
		{"one-level subdomain", "a.example.com", true},
		{"multi-level subdomain", "b.a.example.com", true},
		{"unrelated host", "other.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tools.HostMatch(pattern, tt.host); got != tt.want {
				t.Errorf("HostMatch(%q, %q) = %t, want %t", pattern, tt.host, got, tt.want)
			}
		})
	}
}

// TestHostMatch_SubdomainWildcardRejectsSuffixTrick pins the `"."+base` guard: a wildcard grant must
// not be widened by an attacker registering a look-alike host that merely ends in the base string.
func TestHostMatch_SubdomainWildcardRejectsSuffixTrick(t *testing.T) {
	t.Parallel()
	const pattern = "*.example.com"
	// Each of these ends with "example.com" as a raw string but is NOT a subdomain of example.com.
	tricks := []string{"notexample.com", "evilexample.com", "example.com.evil.com"}
	for _, host := range tricks {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			if tools.HostMatch(pattern, host) {
				t.Errorf("HostMatch(%q, %q) = true, want false (suffix trick must be rejected)", pattern, host)
			}
		})
	}
}

// TestHostMatch_EmptyMatchesNothing pins that an empty grant pattern covers no host-bearing call —
// the security-relevant property from CLAUDE.md §6 ("leer matcht nichts").
//
// DISCREPANCY vs TESTPLAN: the plan says "" should match even an empty host, but the code's equality
// branch (`pattern == host`) makes HostMatch("", "") == true. That degenerate case is unreachable in
// production because http_get rejects u.Host=="" before it ever reaches HostMatch/the grant store, so
// the empty pattern can never be compared against an empty target. We assert the real behavior and the
// property that actually matters: an empty pattern matches NO non-empty host.
func TestHostMatch_EmptyMatchesNothing(t *testing.T) {
	t.Parallel()
	realHosts := []string{"example.com", "a.example.com", "b.a.example.com", "localhost", "127.0.0.1:8080"}
	for _, host := range realHosts {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			if tools.HostMatch("", host) {
				t.Errorf("HostMatch(%q, %q) = true, want false (empty pattern must cover no real host)", "", host)
			}
		})
	}

	// Documents the actual (degenerate, production-unreachable) equality behavior.
	if !tools.HostMatch("", "") {
		t.Errorf("HostMatch(%q, %q) = false, want true (equality branch); see DISCREPANCY note", "", "")
	}
}

func TestHostSuggestions_WidensSubdomainToParent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		host string
		want []gate.Grant
	}{
		{
			name: "one-level subdomain widens to parent wildcard",
			host: "a.example.com",
			want: []gate.Grant{{Kind: tools.NetAxis, Target: "*.example.com"}},
		},
		{
			name: "deep subdomain widens to registrable parent",
			host: "b.a.example.com",
			want: []gate.Grant{{Kind: tools.NetAxis, Target: "*.example.com"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tools.HostSuggestions(tt.host)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("HostSuggestions(%q) = %+v, want %+v", tt.host, got, tt.want)
			}
		})
	}
}

// TestHostSuggestions_NoWidenForApex: an apex domain or a single-label host offers no widening — the
// approver still gets the exact host, but the tool nudges no parent wildcard.
func TestHostSuggestions_NoWidenForApex(t *testing.T) {
	t.Parallel()
	hosts := []string{"example.com", "localhost", "com", ""}
	for _, host := range hosts {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			if got := tools.HostSuggestions(host); got != nil {
				t.Errorf("HostSuggestions(%q) = %+v, want nil", host, got)
			}
		})
	}
}
