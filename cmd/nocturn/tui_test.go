package main

import (
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/brain"
)

// The observer tracks a forest of calls by id: concurrent roots run at once, a
// nested effect folds into its parent when it ends, and a root commits to the
// transcript (carrying its children) only when it itself ends.
func TestHandleToolEvent_ConcurrentRootsAndNesting(t *testing.T) {
	m := chatModel{width: 80}
	ev := func(id, parent uint64, phase brain.Phase, tool string) brain.ToolEvent {
		return brain.ToolEvent{ID: id, Parent: parent, Tool: tool, Phase: phase}
	}

	// Two concurrent roots (1 http.read, 2 code.run); a nested effect (3) under 2.
	m.handleToolEvent(ev(1, 0, brain.ToolStart, "http.read"))
	m.handleToolEvent(ev(2, 0, brain.ToolStart, "code.run"))
	m.handleToolEvent(ev(3, 2, brain.ToolStart, "dns.resolve"))
	if len(m.active) != 3 || len(m.roots) != 2 {
		t.Fatalf("active=%d roots=%d, want 3 active / 2 roots", len(m.active), len(m.roots))
	}

	// The nested effect ends first: it folds into its parent, not into history yet.
	m.handleToolEvent(ev(3, 2, brain.ToolEnd, "dns.resolve"))
	if strings.Contains(m.history, "dns.resolve") {
		t.Fatal("nested effect committed to history before its parent ended")
	}
	if f := m.active[2]; f == nil || len(f.children) != 1 {
		t.Fatalf("nested result was not folded into parent code.run: %+v", f)
	}

	// Root 1 ends → commits to the transcript.
	m.handleToolEvent(ev(1, 0, brain.ToolEnd, "http.read"))
	if !strings.Contains(m.history, "http.read") {
		t.Fatal("finished root http.read not committed to history")
	}

	// Root 2 (code.run) ends → commits, carrying its nested child.
	m.handleToolEvent(ev(2, 0, brain.ToolEnd, "code.run"))
	if !strings.Contains(m.history, "code.run") || !strings.Contains(m.history, "dns.resolve") {
		t.Fatalf("code.run + its nested dns.resolve not in history:\n%s", m.history)
	}
	if len(m.active) != 0 || len(m.roots) != 0 {
		t.Fatalf("active/roots not drained: active=%d roots=%d", len(m.active), len(m.roots))
	}
}
