package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// TestGrantStore_RecallAlways_PersistsAcrossReopen: a RecallAlways grant is written to disk and is
// still honored by a freshly reopened store.
func TestGrantStore_RecallAlways_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	g := gate.Grant{Kind: "net", Target: "example.com"}

	gs, err := newGrantStore(path)
	if err != nil {
		t.Fatalf("newGrantStore: %v", err)
	}
	gs.Remember(g, gate.RecallAlways)

	// The durable grant survives a full reopen (new in-memory set seeded from disk).
	reopened, err := newGrantStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !reopened.Allowed(gate.Action{Kind: "net", Target: "example.com"}, gate.ExactMatch) {
		t.Error("RecallAlways grant did not persist across reopen")
	}
}

// TestGrantStore_RecallSession_NotPersisted: a session grant lives only in memory — it is honored in
// the same process but gone after a reopen.
func TestGrantStore_RecallSession_NotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	g := gate.Grant{Kind: "net", Target: "example.com"}

	gs, err := newGrantStore(path)
	if err != nil {
		t.Fatalf("newGrantStore: %v", err)
	}
	gs.Remember(g, gate.RecallSession)
	if !gs.Allowed(gate.Action{Kind: "net", Target: "example.com"}, gate.ExactMatch) {
		t.Fatal("session grant must be honored within the same store")
	}

	reopened, err := newGrantStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Allowed(gate.Action{Kind: "net", Target: "example.com"}, gate.ExactMatch) {
		t.Error("session grant must NOT persist across reopen")
	}
}

// TestGrantStore_MissingFile_EmptySeed: opening a store where no grants file exists yet is not an
// error and seeds an empty set (nothing is allowed).
func TestGrantStore_MissingFile_EmptySeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	gs, err := newGrantStore(path)
	if err != nil {
		t.Fatalf("newGrantStore on missing file must not error: %v", err)
	}
	if gs.Allowed(gate.Action{Kind: "net", Target: "example.com"}, gate.ExactMatch) {
		t.Error("an empty-seeded store must allow nothing")
	}
}

// TestGrantStore_CorruptFile_Error: a corrupt grants file fails closed (an error), never a silent
// empty set that would drop the operator's standing grants without notice.
func TestGrantStore_CorruptFile_Error(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newGrantStore(path); err == nil {
		t.Fatal("newGrantStore must error on a corrupt grants file (fail-closed)")
	}
}
