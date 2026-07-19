package chat_test

import (
	"context"
	"errors"
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

// newManager builds a manager over store on the test's ctx and guarantees an
// orderly shutdown: CloseAll runs before the test's TempDir is torn down, so no
// pump save races the cleanup.
func newManager(t *testing.T, deps chat.Deps) *chat.Manager {
	t.Helper()
	if deps.Engine == nil {
		deps.Engine = brain.New(okModel{})
	}
	if deps.Guard == nil {
		deps.Guard = &gateway.Guard{}
	}
	if deps.Root == nil {
		deps.Root = func() chat.Charter { return chat.Charter{Tools: tool.NewRegistry()} }
	}
	m := chat.NewManager(t.Context(), deps)
	t.Cleanup(m.CloseAll)
	return m
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
	m := newManager(t, chat.Deps{Store: store})

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
	r.Submit(chat.SourceUser, "hi", "")
	waitTurnEnd(t, sub)

	// The persistence pump saves after the turn (async) — poll the store.
	if !eventually(func() bool {
		msgs, _, _, ok := store.Load(meta.ID)
		return ok && hasTurn(msgs, "user", "hi") && hasTurn(msgs, "assistant", "ok")
	}) {
		t.Fatal("the turn was not persisted to the chat store")
	}
	m.CloseAll()

	// Reopen in a FRESH manager → the history is restored into the runner's snapshot.
	m2 := newManager(t, chat.Deps{Store: chat.LoadStore(dir)})
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
	m := newManager(t, chat.Deps{Store: store})

	meta, err := m.New("Untouched", chat.OriginUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Open(meta.ID); !ok { // it is live (memory-minted), even with no file
		t.Fatal("a memory-minted chat must be openable before it is saved")
	}
	m.CloseAll() // closing an empty chat must not persist it

	if _, _, _, ok := store.Load(meta.ID); ok {
		t.Fatal("an empty chat was written to disk; lazy-persist must skip it")
	}
	if list := store.List(); len(list) != 0 {
		t.Fatalf("store has %d chats, want 0 (nothing took a turn)", len(list))
	}
}

// The unread cursor tracks turns for a LIVE (open) chat: a completed turn advances Updated past
// Read (unread), MarkRead catches Read up to it (read), and the next turn re-flags it. This is the
// end-to-end guard for the cross-device dot — the domain owns Updated/Read on the live Meta, so an
// OPEN chat's List entry reflects new turns (the store is a dumb serializer and never stamps them).
func TestManager_MarkRead_UnreadTracksTurns(t *testing.T) {
	m := newManager(t, chat.Deps{Store: chat.LoadStore(filepath.Join(t.TempDir(), "chats"))})

	meta, err := m.New("c", chat.OriginUser)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := m.Open(meta.ID)
	if !ok {
		t.Fatal("open the just-created chat")
	}
	sub, unsub := r.Subscribe()
	defer unsub()

	find := func() chat.Meta {
		for _, mm := range m.List() {
			if mm.ID == meta.ID {
				return mm
			}
		}
		t.Fatalf("chat %s missing from list", meta.ID)
		return chat.Meta{}
	}
	unread := func(mm chat.Meta) bool { return mm.Read.IsZero() || mm.Updated.After(mm.Read) }

	// A turn runs; the pump advances the live Updated/Turns (touch) → the chat reads as unread.
	r.Submit(chat.SourceUser, "hi", "")
	waitTurnEnd(t, sub)
	if !eventually(func() bool { return find().Turns == 1 }) {
		t.Fatal("the turn was not reflected in the live list Meta")
	}
	if !unread(find()) {
		t.Fatalf("a chat with a turn and no read cursor must be unread: %+v", find())
	}

	// MarkRead catches the cursor up to the latest turn → read (Updated no longer after Read).
	if err := m.MarkRead(meta.ID); err != nil {
		t.Fatal(err)
	}
	if unread(find()) {
		t.Fatalf("after MarkRead the chat must be read: %+v", find())
	}

	// A new turn moves Updated past the cursor → unread again (the dot returns).
	r.Submit(chat.SourceUser, "again", "")
	waitTurnEnd(t, sub)
	if !eventually(func() bool { return unread(find()) }) {
		t.Fatalf("a new turn after MarkRead must re-flag unread: %+v", find())
	}
}

// Deliver drives a specific chat's turn loop (how a fired wake resumes its chat), and is a
// no-op for a deleted/unknown chat — a stale wake never resurrects a removed conversation.
func TestManager_Deliver(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	store := chat.LoadStore(dir)
	m := newManager(t, chat.Deps{Store: store})

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

// A cron firing is a FRESH one-shot chat: FireAgent blocks until its turn completes,
// the transcript persists as the run's audit trail (Origin agent, Meta.Agent set),
// and the record reopens uniformly through Open — under the AGENT charter, which the
// Agent factory resolves (attended: a client asked for it).
func TestManager_FireAgent_PersistsRunAndReopensUnderAgentCharter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	store := chat.LoadStore(dir)
	var resolved []string
	m := newManager(t, chat.Deps{
		Store: store,
		Agent: func(name string) (chat.Charter, error) {
			resolved = append(resolved, name)
			return chat.Charter{Tools: tool.NewRegistry(), System: "agent " + name}, nil
		},
	})

	if err := m.FireAgent(t.Context(), "triage", chat.Charter{Tools: tool.NewRegistry()}); err != nil {
		t.Fatalf("FireAgent: %v", err)
	}
	// FireAgent returns on its own tap's TurnEnd; the pump's save is a parallel tap → poll.
	var run chat.Meta
	if !eventually(func() bool {
		for _, meta := range store.List() {
			if meta.Agent == "triage" {
				run = meta
				return true
			}
		}
		return false
	}) {
		t.Fatal("the firing's transcript was not persisted")
	}
	if run.Origin != chat.OriginAgent || run.Turns != 1 {
		t.Fatalf("run meta = %+v, want Origin=agent Turns=1", run)
	}

	// Uniform reopen: the saved record knows its charter — Open resolves it via the
	// Agent factory (not Root) and restores the run's history.
	c, ok := m.Open(run.ID)
	if !ok {
		t.Fatal("reopen the persisted run")
	}
	if len(resolved) == 0 || resolved[len(resolved)-1] != "triage" {
		t.Fatalf("Open did not resolve the agent charter: resolved=%v", resolved)
	}
	if snap := c.Snapshot(); !hasTurn(snap.Messages, "assistant", "ok") {
		t.Fatalf("reopened run missing its transcript: %+v", snap.Messages)
	}

	// A second firing mints a SECOND fresh record — no memory across firings.
	if err := m.FireAgent(t.Context(), "triage", chat.Charter{Tools: tool.NewRegistry()}); err != nil {
		t.Fatalf("second FireAgent: %v", err)
	}
	if !eventually(func() bool {
		n := 0
		for _, meta := range store.List() {
			if meta.Agent == "triage" {
				n++
			}
		}
		return n == 2
	}) {
		t.Fatal("the second firing did not persist its own fresh record")
	}
}

// A saved run whose agent declaration was deleted still OPENS — the transcript is
// the user's data — but under a zero-authority viewer charter: the history renders,
// while the chat has no tools and no grants (fail-closed on effects, not on reading).
func TestManager_Open_DeletedAgentRun_ViewableWithZeroAuthority(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	store := chat.LoadStore(dir)

	// A persisted run of an agent that no longer exists.
	id := store.NewID()
	msgs := []brain.Message{
		{Role: "user", Content: "Run your scheduled task now."},
		{Role: "assistant", Content: "did the thing"},
	}
	if err := store.Save(chat.Meta{ID: id, Name: "old run", Origin: chat.OriginAgent, Agent: "ghost"}, msgs, nil); err != nil {
		t.Fatal(err)
	}

	var specs []tool.Spec
	m := newManager(t, chat.Deps{
		Store: store,
		Engine: brain.New(modelFunc(func(_ context.Context, _ []brain.Message, s []tool.Spec) (brain.Step, error) {
			specs = s // capture what a NEW turn could reach
			return brain.Step{Answer: "hello from nothing"}, nil
		})),
		Agent: func(name string) (chat.Charter, error) {
			return chat.Charter{}, errors.New("unknown agent " + name)
		},
	})

	c, ok := m.Open(id)
	if !ok {
		t.Fatal("a deleted agent's run must still open — the transcript is the user's data")
	}
	if snap := c.Snapshot(); !hasTurn(snap.Messages, "assistant", "did the thing") {
		t.Fatalf("transcript not restored: %+v", snap.Messages)
	}

	// A new turn is a bare model call: the model sees ZERO tools — every effect path
	// is structurally absent, not merely denied.
	sub, unsub := c.Subscribe()
	defer unsub()
	c.Submit(chat.SourceUser, "and now?", "")
	waitTurnEnd(t, sub)
	if len(specs) != 0 {
		t.Fatalf("viewer chat exposed %d tools, want 0: %+v", len(specs), specs)
	}
}

// Overlap prevention lives in the Manager: while one run of an agent is mid-turn, a
// second FireAgent is skipped with ErrAgentBusy (never queued, never parallel); once
// the run completes, the next firing works again. The turn error propagates.
func TestManager_FireAgent_BusySkip_AndErrorPropagates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chats")
	g := newGatedModel()
	m := newManager(t, chat.Deps{
		Engine: brain.New(g),
		Store:  chat.LoadStore(dir),
		Agent: func(name string) (chat.Charter, error) {
			return chat.Charter{Tools: tool.NewRegistry()}, nil
		},
	})
	ch := chat.Charter{Tools: tool.NewRegistry()}

	done := make(chan error, 1)
	go func() { done <- m.FireAgent(t.Context(), "x", ch) }()
	if !eventually(m.AnyRunning) {
		t.Fatal("the first firing never started its turn")
	}

	if err := m.FireAgent(t.Context(), "x", ch); !errors.Is(err, chat.ErrAgentBusy) {
		t.Fatalf("overlapping firing = %v, want ErrAgentBusy", err)
	}

	g.release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatalf("first firing: %v", err)
	}

	// The agent is free again — and a failing turn surfaces its error to the scheduler.
	bad := errors.New("model down")
	m2 := newManager(t, chat.Deps{
		Engine: brain.New(modelFunc(func(context.Context, []brain.Message, []tool.Spec) (brain.Step, error) {
			return brain.Step{}, bad
		})),
		Store: chat.LoadStore(filepath.Join(t.TempDir(), "chats")),
	})
	if err := m2.FireAgent(t.Context(), "x", ch); !errors.Is(err, bad) {
		t.Fatalf("FireAgent = %v, want the turn's error", err)
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
