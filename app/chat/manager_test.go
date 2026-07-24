package chat_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/app/chat"
)

// fakeLLM answers every turn immediately, so a turn runs end-to-end without a real endpoint.
type fakeLLM struct{}

func (fakeLLM) Next(_ context.Context, _ []agentkit.Message, _ []agentkit.ToolSpec) (agentkit.Step, error) {
	return agentkit.Step{Answer: "ok"}, nil
}

func newManager(t *testing.T) *chat.Manager {
	t.Helper()
	store, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	rt := runtime.New(fakeLLM{})
	m := chat.NewManager(func(string) *runtime.Runtime { return rt }, store, slog.New(slog.DiscardHandler))
	t.Cleanup(m.CloseAll)
	return m
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

// The core guarantee of the server-owned-session design: a submitted turn runs to completion and
// persists even though NO connection is watching (the Manager's pump always drains it), and Open
// returns the SAME live session — it is never duplicated or closed by a viewer coming or going.
func TestManager_TurnSurvivesWithoutViewer_AndOpenShares(t *testing.T) {
	m := newManager(t)

	var mu sync.Mutex
	turnEnds := 0
	m.OnEvent(func(_ string, ev agentkit.Event) {
		if e, ok := ev.(agentkit.TurnEnd); ok && e.Frame == 0 {
			mu.Lock()
			turnEnds++
			mu.Unlock()
		}
	})

	id, sess := m.Start("hi")

	// Open returns the same live session (shared) — a second open never spins a duplicate.
	if got := m.Open(id); got != sess {
		t.Fatal("Open must return the SAME live session as Start (shared, not duplicated)")
	}

	// The turn completes with no one streaming it — the pump drains the session regardless.
	if !eventually(func() bool { mu.Lock(); defer mu.Unlock(); return turnEnds >= 1 }) {
		t.Fatal("turn did not complete without a viewer — the pump must always drain the session")
	}

	// And it persisted (user + assistant messages on disk).
	if msgs, err := m.Transcript(id); err != nil || len(msgs) < 2 {
		t.Fatalf("transcript = %d msgs (err %v), want the turn persisted", len(msgs), err)
	}
}
