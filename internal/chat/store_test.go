package chat_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if err := s.Save(chat.Meta{ID: a, Name: "Trip planning", Origin: chat.OriginUser}, msgs, nil); err != nil {
		t.Fatal(err)
	}

	// Load returns the messages + updated meta (1 user message here) and the origin.
	got, _, meta, ok := s.Load(a)
	if !ok || len(got) != 3 || meta.Turns != 1 || meta.Origin != chat.OriginUser {
		t.Fatalf("load = ok:%v msgs:%d turns:%d origin:%q, want ok/3/1/user", ok, len(got), meta.Turns, meta.Origin)
	}

	b := s.NewID()
	if err := s.Save(chat.Meta{ID: b, Name: "Agent run", Origin: chat.OriginAgent}, []brain.Message{{Role: "user", Content: "x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, m, _ := s.Load(b); m.Origin != chat.OriginAgent {
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
	if _, _, meta, _ := s.Load(a); meta.Name != "Lisbon trip" {
		t.Fatalf("rename not applied: %q", meta.Name)
	}

	if err := s.Delete(b); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 1 {
		t.Fatalf("after delete: %d, want 1", len(s.List()))
	}
}

// Prune caps one agent's saved runs to the keepN most recent — other agents' runs
// and user chats are never touched, so a frequent cron can't flood the picker while
// a root conversation is never at risk.
func TestStore_Prune_KeepsNewestRunsOfThatAgentOnly(t *testing.T) {
	s := chat.LoadStore(filepath.Join(t.TempDir(), "chats"))
	msg := []brain.Message{{Role: "user", Content: "x"}}

	user := s.NewID()
	if err := s.Save(chat.Meta{ID: user, Name: "mine", Origin: chat.OriginUser}, msg, nil); err != nil {
		t.Fatal(err)
	}
	other := s.NewID()
	if err := s.Save(chat.Meta{ID: other, Name: "b run", Origin: chat.OriginAgent, Agent: "b"}, msg, nil); err != nil {
		t.Fatal(err)
	}
	var runs []string
	for range 4 {
		id := s.NewID()
		if err := s.Save(chat.Meta{ID: id, Origin: chat.OriginAgent, Agent: "a"}, msg, nil); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, id)
		time.Sleep(2 * time.Millisecond) // distinct Updated stamps → deterministic recency order
	}

	s.Prune("a", 2)

	kept := map[string]bool{}
	for _, m := range s.List() {
		kept[m.ID] = true
	}
	if !kept[user] || !kept[other] {
		t.Fatalf("Prune touched foreign chats: kept=%v", kept)
	}
	if kept[runs[0]] || kept[runs[1]] || !kept[runs[2]] || !kept[runs[3]] {
		t.Fatalf("Prune must keep the 2 NEWEST a-runs: kept=%v runs=%v", kept, runs)
	}

	// Pruning an empty agent name is a refusal, never a mass delete.
	s.Prune("", 0)
	if len(s.List()) != 4 {
		t.Fatalf("Prune(\"\") deleted chats: %d left, want 4", len(s.List()))
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
		if _, _, _, ok := s.Load(bad); ok {
			t.Fatalf("Load(%q) succeeded — an unsafe id must be rejected", bad)
		}
		if err := s.Save(chat.Meta{ID: bad, Name: "x", Origin: chat.OriginUser}, []brain.Message{{Role: "user", Content: "x"}}, nil); !errors.Is(err, chat.ErrInvalidID) {
			t.Fatalf("Save(%q) = %v, want ErrInvalidID (rejected before the filesystem)", bad, err)
		}
	}
	// The secret is untouched and no stray files were created in root.
	if b, _ := os.ReadFile(secret); string(b) != "top secret" {
		t.Fatal("an unsafe id reached the filesystem")
	}
}

// The tool forest — sub-calls and their errors — round-trips through the store, so a
// reopened chat can render the exact tree it streamed live (not just flat top-level calls).
func TestStore_ForestRoundTrips(t *testing.T) {
	s := chat.LoadStore(filepath.Join(t.TempDir(), "chats"))
	id := s.NewID()
	forest := []chat.ToolFrame{
		{ID: 1, Tool: "code.run", Args: `{"src":"…"}`},
		{ID: 2, Parent: 1, Tool: "http.write", Result: "200 OK"}, // a nested sub-call
		{ID: 3, Parent: 1, Tool: "file.write", Err: "denied"},    // a nested sub-call that errored
	}
	if err := s.Save(chat.Meta{ID: id, Origin: chat.OriginUser}, []brain.Message{{Role: "user", Content: "x"}}, forest); err != nil {
		t.Fatal(err)
	}
	_, got, _, ok := s.Load(id)
	if !ok || len(got) != 3 {
		t.Fatalf("load forest = ok:%v n:%d, want ok/3", ok, len(got))
	}
	if got[1].Parent != 1 || got[2].Err != "denied" {
		t.Fatalf("forest not round-tripped faithfully: %+v", got)
	}
}
