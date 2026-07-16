package capability_test

import (
	"context"
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// call builds a Call on the reach + write axes. mutates=false is a read.
func call(family string, mutates bool, host string) capability.Call {
	c := capability.Call{Family: family, Mutates: mutates}
	if host != "" {
		c.Target = host
	}
	return c
}

func TestCeiling_Allows(t *testing.T) {
	c := capability.NewCeiling(
		capability.Pair{Family: "http", TargetGlob: "*.example.com", Writes: capability.MatchRead},
		capability.Pair{Family: "dns", TargetGlob: "*", Writes: capability.MatchRead},
	)
	cases := []struct {
		family  string
		mutates bool
		host    string
		want    bool
	}{
		{"http", false, "api.example.com", true},
		{"http", false, "evil.com", false},        // host outside
		{"http", true, "api.example.com", false},  // write outside a read-only reach
		{"dns", false, "anything", true},          // wildcard host, read
	}
	for _, tc := range cases {
		if got := c.Allows(call(tc.family, tc.mutates, tc.host)); got != tc.want {
			t.Errorf("Allows(%s mutates=%v %s) = %v, want %v", tc.family, tc.mutates, tc.host, got, tc.want)
		}
	}
}

func TestCeiling_EmptyAllowsNothing(t *testing.T) {
	if capability.NewCeiling().Allows(call("http", false, "example.com")) {
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
	if !capability.WithinCeilings(context.Background(), call("http", true, "anywhere")) {
		t.Fatal("with no ceiling, every call must be within")
	}
}

// The chain intersects: a call must satisfy EVERY ceiling. Appending a tighter
// inner ceiling can only subtract.
func TestCeilingChain_Intersects(t *testing.T) {
	outer := capability.NewCeiling(
		capability.Pair{Family: "http", TargetGlob: "*.example.com", Writes: capability.MatchAny},
	)
	inner := capability.NewCeiling(
		capability.Pair{Family: "http", TargetGlob: "api.example.com", Writes: capability.MatchRead}, // read only, one host
	)
	ctx := capability.WithCeiling(capability.WithCeiling(context.Background(), outer), inner)

	if !capability.WithinCeilings(ctx, call("http", false, "api.example.com")) {
		t.Fatal("read to api.example.com is allowed by both ceilings")
	}
	if capability.WithinCeilings(ctx, call("http", true, "api.example.com")) {
		t.Fatal("write is allowed by outer but NOT inner → intersection denies")
	}
	if capability.WithinCeilings(ctx, call("http", false, "other.example.com")) {
		t.Fatal("read to other host allowed by outer but NOT inner → denied")
	}
}
