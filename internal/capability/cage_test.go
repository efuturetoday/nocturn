package capability_test

import (
	"context"
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// call builds a Call on the reach + write axes. write=false is a read.
func call(family string, write bool, host string) capability.Call {
	c := capability.Call{Family: family, Write: write}
	if host != "" {
		c.Target = host
	}
	return c
}

func TestCage_Allows(t *testing.T) {
	c := capability.NewCage(
		capability.Pair{Family: "http", TargetGlob: "*.example.com", Writes: capability.MatchRead},
		capability.Pair{Family: "dns", TargetGlob: "*", Writes: capability.MatchRead},
	)
	cases := []struct {
		family string
		write  bool
		host   string
		want   bool
	}{
		{"http", false, "api.example.com", true},
		{"http", false, "evil.com", false},       // host outside
		{"http", true, "api.example.com", false}, // write outside a read-only reach
		{"dns", false, "anything", true},         // wildcard host, read
	}
	for _, tc := range cases {
		if got := c.Allows(call(tc.family, tc.write, tc.host)); got != tc.want {
			t.Errorf("Allows(%s write=%v %s) = %v, want %v", tc.family, tc.write, tc.host, got, tc.want)
		}
	}
}

func TestCage_EmptyAllowsNothing(t *testing.T) {
	if capability.NewCage().Allows(call("http", false, "example.com")) {
		t.Fatal("empty cage must allow nothing")
	}
}

// No cage in ctx = unrestricted caller (model/script): WithinCages is
// vacuously true.
// An empty chain is vacuously within — WithinCages is deliberately fail-OPEN
// when no cage was stamped. That is safe ONLY because a cage is not the
// primary gate: the base policy still governs (deny-by-default for anything it
// doesn't Allow), and every caller that MUST be bounded (plugins) stamps its
// cage before any effect can reach the broker (see plugin.runGuest, the sole
// stamping site). This test pins that intent so a future "fail-closed by default"
// change is a conscious decision, not an accident.
func TestWithinCages_NoneIsVacuouslyTrue(t *testing.T) {
	if !capability.WithinCages(context.Background(), call("http", true, "anywhere")) {
		t.Fatal("with no cage, every call must be within")
	}
}

// The chain intersects: a call must satisfy EVERY cage. Appending a tighter
// inner cage can only subtract.
func TestCageChain_Intersects(t *testing.T) {
	outer := capability.NewCage(
		capability.Pair{Family: "http", TargetGlob: "*.example.com", Writes: capability.MatchAny},
	)
	inner := capability.NewCage(
		capability.Pair{Family: "http", TargetGlob: "api.example.com", Writes: capability.MatchRead}, // read only, one host
	)
	ctx := capability.WithCage(capability.WithCage(context.Background(), outer), inner)

	if !capability.WithinCages(ctx, call("http", false, "api.example.com")) {
		t.Fatal("read to api.example.com is allowed by both cages")
	}
	if capability.WithinCages(ctx, call("http", true, "api.example.com")) {
		t.Fatal("write is allowed by outer but NOT inner → intersection denies")
	}
	if capability.WithinCages(ctx, call("http", false, "other.example.com")) {
		t.Fatal("read to other host allowed by outer but NOT inner → denied")
	}
}
