package capability_test

import (
	"context"
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
)

func call(cap, host string) capability.Call {
	c := capability.Call{Capability: cap}
	if host != "" {
		c.Attrs = map[string]string{"host": host}
	}
	return c
}

func TestCeiling_Allows(t *testing.T) {
	c := capability.NewCeiling(
		capability.Pair{Capability: "http.read", HostGlob: "*.example.com"},
		capability.Pair{Capability: "dns.resolve", HostGlob: "*"},
	)
	cases := []struct {
		cap, host string
		want      bool
	}{
		{"http.read", "api.example.com", true},
		{"http.read", "evil.com", false},         // host outside
		{"http.write", "api.example.com", false}, // capability outside
		{"dns.resolve", "anything", true},        // wildcard host
	}
	for _, tc := range cases {
		if got := c.Allows(call(tc.cap, tc.host)); got != tc.want {
			t.Errorf("Allows(%s,%s) = %v, want %v", tc.cap, tc.host, got, tc.want)
		}
	}
}

func TestCeiling_EmptyAllowsNothing(t *testing.T) {
	if capability.NewCeiling().Allows(call("http.read", "example.com")) {
		t.Fatal("empty ceiling must allow nothing")
	}
}

// No ceiling in ctx = unrestricted caller (model/script): WithinCeilings is
// vacuously true.
// An empty chain is vacuously within — WithinCeilings is deliberately fail-OPEN
// when no ceiling was stamped. That is safe ONLY because a ceiling is not the
// primary gate: the base policy still governs (deny-by-default for anything it
// doesn't Allow), and every caller that MUST be bounded (plugins) stamps its
// ceiling before any effect can reach the broker (see plugin.runGuest, the sole
// stamping site). This test pins that intent so a future "fail-closed by default"
// change is a conscious decision, not an accident.
func TestWithinCeilings_NoneIsVacuouslyTrue(t *testing.T) {
	if !capability.WithinCeilings(context.Background(), call("http.write", "anywhere")) {
		t.Fatal("with no ceiling, every call must be within")
	}
}

// The chain intersects: a call must satisfy EVERY ceiling. Appending a tighter
// inner ceiling can only subtract.
func TestCeilingChain_Intersects(t *testing.T) {
	outer := capability.NewCeiling(
		capability.Pair{Capability: "http.read", HostGlob: "*.example.com"},
		capability.Pair{Capability: "http.write", HostGlob: "*.example.com"},
	)
	inner := capability.NewCeiling(
		capability.Pair{Capability: "http.read", HostGlob: "api.example.com"}, // read only, one host
	)
	ctx := capability.WithCeiling(capability.WithCeiling(context.Background(), outer), inner)

	if !capability.WithinCeilings(ctx, call("http.read", "api.example.com")) {
		t.Fatal("read to api.example.com is allowed by both ceilings")
	}
	if capability.WithinCeilings(ctx, call("http.write", "api.example.com")) {
		t.Fatal("write is allowed by outer but NOT inner → intersection denies")
	}
	if capability.WithinCeilings(ctx, call("http.read", "other.example.com")) {
		t.Fatal("read to other host allowed by outer but NOT inner → denied")
	}
}
