package chat_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/session"
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

	meta, err := m.New("Chat 1")
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
	r.Submit(session.SourceUser, "hi")
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

func waitTurnEnd(t *testing.T, sub <-chan session.Event) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-sub:
			if _, ok := e.(session.TurnEndEvent); ok {
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
