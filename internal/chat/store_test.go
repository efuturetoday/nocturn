package chat_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/chat"
)

func TestStore_SaveLoadListRenameDelete(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	s := chat.LoadStore(dir)

	if len(s.List()) != 0 {
		t.Fatal("fresh store must be empty")
	}

	// A chat exists on disk only once saved (lazy-persist): mint an id, then Save creates it.
	a := s.NewID()
	msgs := []brain.Message{
		{Role: "system", Content: "persona"},
		{Role: "user", Content: "where to?"},
		{Role: "assistant", Content: "Lisbon"},
	}
	if err := s.Save(a, "Trip planning", chat.OriginUser, msgs); err != nil {
		t.Fatal(err)
	}

	// Load returns the messages + updated meta (1 user message here) and the origin.
	got, meta, ok := s.Load(a)
	if !ok || len(got) != 3 || meta.Turns != 1 || meta.Origin != chat.OriginUser {
		t.Fatalf("load = ok:%v msgs:%d turns:%d origin:%q, want ok/3/1/user", ok, len(got), meta.Turns, meta.Origin)
	}

	b := s.NewID()
	if err := s.Save(b, "Agent run", chat.OriginAgent, []brain.Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, m, _ := s.Load(b); m.Origin != chat.OriginAgent {
		t.Fatalf("origin = %q, want agent", m.Origin)
	}

	// List: most-recently-updated first → b (saved last) before a.
	list := s.List()
	if len(list) != 2 || list[0].ID != b {
		t.Fatalf("list = %+v, want b first (most recent)", list)
	}

	if err := s.Rename(a, "Lisbon trip"); err != nil {
		t.Fatal(err)
	}
	if _, meta, _ := s.Load(a); meta.Name != "Lisbon trip" {
		t.Fatalf("rename not applied: %q", meta.Name)
	}

	if err := s.Delete(b); err != nil {
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
		if err := s.Save(bad, "x", chat.OriginUser, []brain.Message{{Role: "user", Content: "x"}}); !errors.Is(err, chat.ErrInvalidID) {
			t.Fatalf("Save(%q) = %v, want ErrInvalidID (rejected before the filesystem)", bad, err)
		}
	}
	// The secret is untouched and no stray files were created in root.
	if b, _ := os.ReadFile(secret); string(b) != "top secret" {
		t.Fatal("an unsafe id reached the filesystem")
	}
}
