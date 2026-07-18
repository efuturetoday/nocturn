package chat_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// okModel is a trivial brain.Model that answers immediately, so a turn runs end to end
// without a real endpoint.
type okModel struct{}

func (okModel) Next(context.Context, []brain.Message, []tool.Spec) (brain.Step, error) {
	return brain.Step{Answer: "ok"}, nil
}

func newManager(ctx context.Context, store *chat.Store) *chat.Manager {
	return chat.NewManager(ctx, chat.Deps{
		Brain:    brain.New(okModel{}),
		Tools:    tool.NewRegistry(),
		Guard:    &gateway.Guard{},
		Persona:  func() string { return "" },
		Store:    store,
		AgentRun: func(context.Context, string, string) (string, error) { return "", nil },
	})
}

func hasTurn(msgs []brain.Message, role, content string) bool {
	for _, m := range msgs {
		if m.Role == role && m.Content == content {
			return true
		}
	}
	return false
}

// A chat runs a turn, the persistence pump saves it, and reopening the chat (even in a fresh
// manager) restores its history — the full multi-chat runtime round trip.
func TestManager_OpenPersistReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	store := chat.LoadStore(dir)
	m := newManager(t.Context(), store)

	meta, err := m.New("Chat 1", chat.OriginUser)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := m.Open(meta.ID)
	if !ok {
		t.Fatal("open the just-created chat")
	}

	// Run one turn through the live runner.
	sub, unsub := r.Subscribe()
	defer unsub()
	r.Submit(chat.SourceUser, "hi")
	waitTurnEnd(t, sub)

	// The persistence pump saves after the turn (async) — poll the store.
	if !eventually(func() bool {
		msgs, _, ok := store.Load(meta.ID)
		return ok && hasTurn(msgs, "user", "hi") && hasTurn(msgs, "assistant", "ok")
	}) {
		t.Fatal("the turn was not persisted to the chat store")
	}
	m.CloseAll()

	// Reopen in a FRESH manager → the history is restored into the runner's snapshot.
	m2 := newManager(t.Context(), chat.LoadStore(dir))
	r2, ok := m2.Open(meta.ID)
	if !ok {
		t.Fatal("reopen the persisted chat")
	}
	snap := r2.Snapshot()
	if !hasTurn(snap.Messages, "user", "hi") || !hasTurn(snap.Messages, "assistant", "ok") {
		t.Fatalf("reopened chat missing its history: %+v", snap.Messages)
	}
}

// Lazy-persist: a chat that never takes a turn is never written to disk, so an untouched
// New (a TUI launch the user closes without typing) leaves no empty file behind.
func TestManager_LazyPersist_EmptyChatNeverWritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	store := chat.LoadStore(dir)
	m := newManager(t.Context(), store)

	meta, err := m.New("Untouched", chat.OriginUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Open(meta.ID); !ok { // it is live (memory-minted), even with no file
		t.Fatal("a memory-minted chat must be openable before it is saved")
	}
	m.CloseAll() // closing an empty chat must not persist it

	if _, _, ok := store.Load(meta.ID); ok {
		t.Fatal("an empty chat was written to disk; lazy-persist must skip it")
	}
	if list := store.List(); len(list) != 0 {
		t.Fatalf("store has %d chats, want 0 (nothing took a turn)", len(list))
	}
}

// Deliver drives a specific chat's turn loop (how a fired wake resumes its chat), and is a
// no-op for a deleted/unknown chat — a stale wake never resurrects a removed conversation.
func TestManager_Deliver(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	store := chat.LoadStore(dir)
	m := newManager(t.Context(), store)

	meta, err := m.New("c", chat.OriginUser)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := m.Open(meta.ID)
	sub, unsub := r.Subscribe()
	defer unsub()

	m.Deliver(meta.ID, chat.SourceWake, "resume me")
	waitTurnEnd(t, sub)
	if snap := r.Snapshot(); !hasTurn(snap.Messages, "user", "resume me") {
		t.Fatalf("Deliver did not drive the chat's turn: %+v", snap.Messages)
	}

	// Unknown/deleted id: a no-op, not a panic and not a resurrection.
	m.Deliver("deadbeef", chat.SourceWake, "ghost")
	if _, ok := m.Open("deadbeef"); ok {
		t.Fatal("Deliver resurrected an unknown chat")
	}
}

func waitTurnEnd(t *testing.T, sub <-chan chat.Event) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-sub:
			if _, ok := e.(chat.TurnEndEvent); ok {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for TurnEnd")
		}
	}
}

func eventually(cond func() bool) bool {
	for range 200 {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
