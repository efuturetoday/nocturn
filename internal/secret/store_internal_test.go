package secret

import (
	"bytes"
	"testing"
)

// TestStore_Snapshot_IsCopy proves snapshot returns a map the caller may mutate
// without disturbing the store — the Vault relies on this to serialize without
// racing later Sets. Internal test: snapshot is host-only.
func TestStore_Snapshot_IsCopy(t *testing.T) {
	s := NewStore()
	s.Set("a", []byte("value-a"))

	snap := s.snapshot()
	if got := snap["a"]; !bytes.Equal(got, []byte("value-a")) {
		t.Fatalf("snapshot missing entry: got %q", got)
	}

	// Mutating the returned map must not touch the store.
	snap["a"] = []byte("tampered")
	snap["b"] = []byte("added")
	delete(snap, "a")

	if v, ok := s.value("a"); !ok || !bytes.Equal(v, []byte("value-a")) {
		t.Fatalf("store mutated through snapshot: got %q ok=%v", v, ok)
	}
	if _, ok := s.value("b"); ok {
		t.Fatal("adding to snapshot leaked into the store")
	}
}
