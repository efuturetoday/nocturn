package chat_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/chat"
)

func TestStore_CreateSaveLoadListRenameDelete(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	s := chat.LoadStore(dir)

	if len(s.List()) != 0 {
		t.Fatal("fresh store must be empty")
	}

	a, err := s.Create("Trip planning")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create("") // default name
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "New chat" {
		t.Fatalf("default name = %q, want 'New chat'", b.Name)
	}

	// Save messages into chat a.
	msgs := []brain.Message{
		{Role: "system", Content: "persona"},
		{Role: "user", Content: "where to?"},
		{Role: "assistant", Content: "Lisbon"},
	}
	if err := s.Save(a.ID, "", msgs); err != nil {
		t.Fatal(err)
	}

	// Load returns the messages + updated meta (2 turns... 1 user message here).
	got, meta, ok := s.Load(a.ID)
	if !ok || len(got) != 3 || meta.Turns != 1 {
		t.Fatalf("load = ok:%v msgs:%d turns:%d, want ok/3/1", ok, len(got), meta.Turns)
	}

	// List: most-recently-updated first → a (just saved) before b.
	list := s.List()
	if len(list) != 2 || list[0].ID != a.ID {
		t.Fatalf("list = %+v, want a first (most recent)", list)
	}

	if err := s.Rename(a.ID, "Lisbon trip"); err != nil {
		t.Fatal(err)
	}
	if _, meta, _ := s.Load(a.ID); meta.Name != "Lisbon trip" {
		t.Fatalf("rename not applied: %q", meta.Name)
	}

	if err := s.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 1 {
		t.Fatalf("after delete: %d, want 1", len(s.List()))
	}
}

// A malformed / traversal chat id is rejected before touching the filesystem — it can never
// escape the chats directory.
func TestStore_RejectsUnsafeID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "chats")
	s := chat.LoadStore(dir)

	// A sibling secret outside the chats dir.
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"../secret", "..", "a/b", "ABC", "with.dot", ""} {
		if _, _, ok := s.Load(bad); ok {
			t.Fatalf("Load(%q) succeeded — an unsafe id must be rejected", bad)
		}
		if err := s.Save(bad, "x", []brain.Message{{Role: "user", Content: "x"}}); err != nil {
			t.Fatalf("Save(%q) errored instead of no-op: %v", bad, err)
		}
	}
	// The secret is untouched and no stray files were created in root.
	if b, _ := os.ReadFile(secret); string(b) != "top secret" {
		t.Fatal("an unsafe id reached the filesystem")
	}
}
