package workspace

import (
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// A standing approval is authority that outlives the moment it was given, so a person has to be able
// to SEE it — and the durable/session distinction is what makes the list judgeable: "until this
// daemon stops" and "forever" are different answers, and only the second accumulates.
func TestGrantStore_ListMarksWhatSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	s, err := newGrantStore(path)
	if err != nil {
		t.Fatal(err)
	}

	s.Remember(gate.Grant{Kind: "net", Target: "api.example.com"}, gate.RecallAlways)
	s.Remember(gate.Grant{Kind: "net", Target: "b.example.com"}, gate.RecallSession)
	s.Remember(gate.Grant{Kind: "file", Target: "notes/*"}, gate.RecallAlways)

	got := s.List()
	if len(got) != 3 {
		t.Fatalf("List() = %+v, want all three standing grants", got)
	}
	// Sorted by kind then target, so a list on a screen does not reshuffle between reads.
	if got[0].Grant.Kind != "file" || got[1].Grant.Target != "api.example.com" {
		t.Errorf("List() = %+v, want it sorted by kind then target", got)
	}
	durable := map[string]bool{}
	for _, st := range got {
		durable[st.Grant.Target] = st.Durable
	}
	if !durable["api.example.com"] || durable["b.example.com"] || !durable["notes/*"] {
		t.Errorf("durability = %v, want only the RecallAlways ones marked", durable)
	}
}

// A revoked grant has to be gone from BOTH halves, or it returns at the next start — the worst kind
// of revocation, because nobody is watching when it lapses.
func TestGrantStore_ForgetSurvivesAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	s, err := newGrantStore(path)
	if err != nil {
		t.Fatal(err)
	}
	g := gate.Grant{Kind: "net", Target: "api.example.com"}
	s.Remember(g, gate.RecallAlways)

	if !s.Forget(g) {
		t.Fatal("Forget() reported nothing to forget")
	}
	if len(s.List()) != 0 {
		t.Errorf("List() = %+v after forgetting the only grant", s.List())
	}

	reopened, err := newGrantStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.List(); len(got) != 0 {
		t.Errorf("a revoked grant came back at the next start: %+v", got)
	}
}
