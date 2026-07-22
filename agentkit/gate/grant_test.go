package gate

// White-box (package gate): the set-dedup case asserts the internal map does not grow, which is not
// observable through the public API. The remaining cases exercise the public Grants contract.

import (
	"sync"
	"testing"
)

// ExactMatch is the opaque default: "*" is a wildcard, otherwise plain equality — no host/path
// semantics.
func TestExactMatch_StarAndEquality(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		target  string
		want    bool
	}{
		{"wildcard matches anything", "*", "example.com", true},
		{"wildcard matches empty", "*", "", true},
		{"exact equality", "example.com", "example.com", true},
		{"inequality", "example.com", "other.com", false},
		{"no suffix semantics", "*.example.com", "api.example.com", false},
		{"empty pattern matches only empty", "", "", true},
		{"empty pattern rejects nonempty", "", "x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExactMatch(tt.pattern, tt.target); got != tt.want {
				t.Errorf("ExactMatch(%q, %q) = %v, want %v", tt.pattern, tt.target, got, tt.want)
			}
		})
	}
}

// A remembered grant covers a matching action; remembering the same grant again is idempotent — the
// backing set does not grow (it is keyed by the Grant value).
func TestMemGrants_AllowedAndRemember(t *testing.T) {
	m := NewMemGrants()
	g := Grant{Kind: "net", Target: "example.com"}
	a := Action{Kind: "net", Target: "example.com"}

	if m.Allowed(a, nil) {
		t.Fatal("fresh store already allows the action")
	}
	m.Remember(g, RecallSession)
	if !m.Allowed(a, nil) {
		t.Fatal("action not allowed after Remember")
	}

	// Repeated Remember of the same grant must not accumulate.
	m.Remember(g, RecallAlways)
	m.Remember(g, RecallSession)
	if n := len(m.set); n != 1 {
		t.Fatalf("set grew to %d after repeated Remember of one grant, want 1", n)
	}
}

// A grant with Kind "*" covers any Kind (Kind matching is structural and done by gate itself).
func TestMemGrants_KindWildcard(t *testing.T) {
	m := NewMemGrants(Grant{Kind: "*", Target: "example.com"})
	if !m.Allowed(Action{Kind: "net", Target: "example.com"}, nil) {
		t.Fatal("wildcard-Kind grant did not cover net action")
	}
	if !m.Allowed(Action{Kind: "dns", Target: "example.com"}, nil) {
		t.Fatal("wildcard-Kind grant did not cover dns action")
	}
	if m.Allowed(Action{Kind: "net", Target: "other.com"}, nil) {
		t.Fatal("wildcard-Kind grant wrongly covered a different Target")
	}
}

// Target semantics come from the supplied Matcher, not from gate: a suffix matcher lets one grant cover
// a whole host subtree.
func TestMemGrants_UsesSuppliedMatcher(t *testing.T) {
	suffix := func(pattern, target string) bool {
		return pattern == target || (len(pattern) > 2 && pattern[:2] == "*." &&
			len(target) >= len(pattern)-1 && target[len(target)-(len(pattern)-1):] == pattern[1:])
	}
	m := NewMemGrants(Grant{Kind: "net", Target: "*.example.com"})

	if !m.Allowed(Action{Kind: "net", Target: "api.example.com"}, suffix) {
		t.Fatal("suffix matcher did not cover api.example.com")
	}
	// The same grant with the default ExactMatch does NOT cover it — proving the matcher is what decides.
	if m.Allowed(Action{Kind: "net", Target: "api.example.com"}, nil) {
		t.Fatal("ExactMatch wrongly covered api.example.com via a *.-pattern")
	}
}

// A nil Matcher defaults to ExactMatch.
func TestMemGrants_NilMatcher_DefaultsExact(t *testing.T) {
	m := NewMemGrants(Grant{Kind: "net", Target: "example.com"})
	if !m.Allowed(Action{Kind: "net", Target: "example.com"}, nil) {
		t.Fatal("nil matcher did not behave as ExactMatch for an equal target")
	}
	if m.Allowed(Action{Kind: "net", Target: "example.org"}, nil) {
		t.Fatal("nil matcher matched an unequal target")
	}
}

// The caller-supplied Matcher runs OUTSIDE the store lock (Allowed snapshots first), so a Matcher that
// re-enters the store cannot deadlock.
func TestMemGrants_CustomMatcher_RunOutsideLock(t *testing.T) {
	m := NewMemGrants(Grant{Kind: "net", Target: "example.com"})
	reentrant := func(pattern, target string) bool {
		// Re-enter the store from within the matcher; must not deadlock.
		m.Remember(Grant{Kind: "misc", Target: "side-effect"}, RecallSession)
		return pattern == target
	}

	done := make(chan bool, 1)
	go func() { done <- m.Allowed(Action{Kind: "net", Target: "example.com"}, reentrant) }()
	if !<-done {
		t.Fatal("reentrant matcher did not report a match")
	}
}

// Concurrent Remember and Allowed are race-free (run under -race).
func TestMemGrants_ConcurrentRememberAllowed_NoRace(t *testing.T) {
	m := NewMemGrants()
	a := Action{Kind: "net", Target: "example.com"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Go(func() { m.Remember(Grant{Kind: "net", Target: "example.com"}, RecallSession) })
		wg.Go(func() { _ = m.Allowed(a, nil) })
	}
	wg.Wait()

	if !m.Allowed(a, nil) {
		t.Fatal("action not allowed after concurrent Remembers")
	}
	if n := len(m.set); n != 1 {
		t.Fatalf("set holds %d grants after concurrent identical Remembers, want 1", n)
	}
}
