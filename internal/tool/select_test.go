package tool_test

import (
	"context"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/internal/tool"
)

// Reproduces the stack-overflow crash: a filtered (Select'd) registry and the
// shared one used to keep SEPARATE id counters, so a nested call through the
// shared registry got the SAME id its parent got from the filtered view →
// frame.parent == frame.id → the observer forest cycled and the TUI recursed
// forever. A shared counter keeps every id unique.
func TestRegistry_Select_NestedCall_NoIDCollision(t *testing.T) {
	var mu sync.Mutex
	var starts []tool.Event
	rec := func(ev tool.Event) {
		if ev.Phase == tool.Start {
			mu.Lock()
			starts = append(starts, ev)
			mu.Unlock()
		}
	}

	var shared *tool.Registry
	shared = tool.NewRegistry([]tool.Tool{
		{Spec: tool.Spec{Name: "inner"}, Invoke: func(context.Context, string) (string, error) { return "ok", nil }},
		{Spec: tool.Spec{Name: "outer"}, Invoke: func(ctx context.Context, _ string) (string, error) {
			return shared.Invoke(ctx, "inner", "{}") // the "plugin" re-enters the shared registry
		}},
	})
	shared.OnCall = rec

	// The model calls "outer" through a FILTERED view; "outer" nests "inner".
	filtered := shared.Select(func(name string) bool { return name == "outer" })
	if _, err := filtered.Invoke(context.Background(), "outer", "{}"); err != nil {
		t.Fatalf("invoke: %v", err)
	}

	if len(starts) != 2 {
		t.Fatalf("got %d start events, want 2 (outer, inner)", len(starts))
	}
	outer, inner := starts[0], starts[1]
	if inner.Parent != outer.ID {
		t.Fatalf("inner.Parent=%d, want outer.ID=%d", inner.Parent, outer.ID)
	}
	if inner.ID == outer.ID {
		t.Fatalf("ID COLLISION: outer.ID == inner.ID == %d → the forest would cycle", inner.ID)
	}
}
