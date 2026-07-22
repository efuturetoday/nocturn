package chat

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

// newForest/start/end/snapshot/inflight are unexported; these unit tests exercise the accumulator
// directly (the pure logic the manager drives from the live event stream).

func TestForest_StartOrder_ParentsBeforeChildren(t *testing.T) {
	f := newForest()
	f.start(1, 0, "code_run", "{}")
	f.start(2, 1, "http_read", "{}") // child of 1
	f.start(3, 0, "time_now", "{}")

	snap := f.snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
	// First-seen (start) order, so a parent always precedes its child.
	if snap[0].ID != 1 || snap[1].ID != 2 || snap[2].ID != 3 {
		t.Fatalf("order = %d,%d,%d, want 1,2,3", snap[0].ID, snap[1].ID, snap[2].ID)
	}
	if snap[1].Parent != 1 {
		t.Errorf("node 2 parent = %d, want 1", snap[1].Parent)
	}
}

func TestForest_Start_Idempotent(t *testing.T) {
	f := newForest()
	f.start(1, 0, "probe", "{}")
	f.start(1, 0, "probe-again", "{}") // same id — must not overwrite or reorder
	snap := f.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1 (start is create-if-absent)", len(snap))
	}
	if snap[0].Tool != "probe" {
		t.Errorf("tool = %q, want the first start to win", snap[0].Tool)
	}
}

func TestForest_End_FillsResult_MissingStartIgnored(t *testing.T) {
	f := newForest()
	f.start(1, 0, "probe", "{}")
	f.end(1, "result-body", "", 42)
	f.end(99, "orphan", "", 1) // no matching start — ignored, no panic

	snap := f.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if snap[0].Result != "result-body" || snap[0].DurationMs != 42 {
		t.Errorf("node = %+v, want result and duration filled", snap[0])
	}
}

func TestForest_Snapshot_EmptyForest(t *testing.T) {
	if snap := newForest().snapshot(); len(snap) != 0 {
		t.Fatalf("empty forest snapshot = %+v, want empty", snap)
	}
}

func TestErrText_NilAndNonNil(t *testing.T) {
	if got := errText(nil); got != "" {
		t.Errorf("errText(nil) = %q, want empty", got)
	}
	if got := errText(errors.New("boom")); got != "boom" {
		t.Errorf("errText(err) = %q, want the error string", got)
	}
}

// observe drives the in-flight state from the session's own event stream. These tests feed it
// hand-crafted events (the same shapes the live stream produces, including sub-agent frames) and
// assert the read-model it maintains.

func newObserveManager(t *testing.T) (*Manager, *Store) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Manager{store: st, log: slog.New(slog.DiscardHandler)}, st
}

// observe buffers the running turn's events verbatim (any frame) for a mid-turn reopen — it does not
// interpret them into a render model; the client folds them. Events before TurnStart (no running turn)
// are dropped; TurnEnd(frame 0) clears the buffer (tested elsewhere via Inflight going zero).
func TestManager_observe_BuffersRunningTurnEvents(t *testing.T) {
	m, _ := newObserveManager(t)
	lv := &live{}

	// Before any TurnStart there is no running turn — this event is not buffered.
	m.observe("id", lv, agentkit.Token{Frame: 0, Text: "stray"})
	if len(lv.events) != 0 {
		t.Fatalf("events = %+v, want empty before TurnStart", lv.events)
	}

	// From TurnStart(frame 0) on, every event (top-level AND sub-agent frames) is buffered in order.
	m.observe("id", lv, agentkit.TurnStart{Frame: 0})
	m.observe("id", lv, agentkit.Token{Frame: 0, Text: "top"})
	m.observe("id", lv, agentkit.Thinking{Frame: 1, Text: "sub-agent reasoning"})
	m.observe("id", lv, agentkit.ToolStart{Frame: 0, ID: 1, Tool: "probe"})

	if len(lv.events) != 4 {
		t.Fatalf("buffered %d events, want 4 (TurnStart + token + thinking + toolStart)", len(lv.events))
	}
	if _, ok := lv.events[0].(agentkit.TurnStart); !ok {
		t.Errorf("events[0] = %T, want TurnStart (the buffer opens with the turn)", lv.events[0])
	}
	if tk, ok := lv.events[1].(agentkit.Token); !ok || tk.Text != "top" {
		t.Errorf("events[1] = %+v, want Token{top}", lv.events[1])
	}

	// A fresh TurnStart(frame 0) resets the buffer for the next turn.
	m.observe("id", lv, agentkit.TurnStart{Frame: 0})
	if len(lv.events) != 1 {
		t.Fatalf("events after a new TurnStart = %d, want 1 (buffer reset)", len(lv.events))
	}
}

func TestManager_observe_ToolStart_AnyFrameCaptured(t *testing.T) {
	m, _ := newObserveManager(t)
	lv := &live{forest: newForest()}
	m.observe("id", lv, agentkit.TurnStart{Frame: 0})
	// A nested/sub-agent tool call carries a non-zero frame; the forest still captures it.
	m.observe("id", lv, agentkit.ToolStart{Frame: 7, ID: 3, Tool: "nested", Args: "{}"})

	if _, ok := lv.forest.nodes[3]; !ok {
		t.Fatal("ToolStart on a non-zero frame was not captured in the forest")
	}
	if lv.forest.nodes[3].Parent != 7 {
		t.Errorf("captured parent = %d, want 7 (the enclosing frame)", lv.forest.nodes[3].Parent)
	}
}

func TestManager_observe_ToolBeforeTurnStart_NilForestNoPanic(t *testing.T) {
	m, _ := newObserveManager(t)
	lv := &live{} // no TurnStart yet → forest is nil
	// Must not panic (the observe guards each forest access on non-nil).
	m.observe("id", lv, agentkit.ToolStart{Frame: 0, ID: 1, Tool: "x"})
	m.observe("id", lv, agentkit.ToolEnd{Frame: 0, ID: 1, Result: "r"})
	if lv.forest != nil {
		t.Error("forest should still be nil — no TurnStart opened it")
	}
}

// A turn with no tool calls still appends an EMPTY group at TurnEnd, keeping the per-turn tool forest
// index-aligned with the turns.
func TestManager_EmptyGroupAppendedForNoToolTurn(t *testing.T) {
	m, st := newObserveManager(t)
	const id = "aa00bb"
	if err := st.Save(id, []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	lv := &live{}
	m.observe(id, lv, agentkit.TurnStart{Frame: 0})
	m.observe(id, lv, agentkit.TurnEnd{Frame: 0}) // no tools this turn

	groups, err := st.LoadTools(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 empty group appended for index alignment", len(groups))
	}
	if len(groups[0]) != 0 {
		t.Errorf("group = %+v, want empty", groups[0])
	}
}

// Nested and sub-agent tool calls (non-zero frames) are captured with correct parent links, in
// parents-before-children order, and persisted at TurnEnd. A sub-agent's own TurnStart/TurnEnd (a
// non-zero frame) must NOT reset or close the top-level turn.
func TestManager_NestedAndSubAgentCallsCaptured(t *testing.T) {
	m, st := newObserveManager(t)
	const id = "aa01bb"
	if err := st.Save(id, []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	lv := &live{}
	m.observe(id, lv, agentkit.TurnStart{Frame: 0})
	m.observe(id, lv, agentkit.ToolStart{Frame: 0, ID: 1, Tool: "code_run"})
	m.observe(id, lv, agentkit.ToolStart{Frame: 1, ID: 2, Tool: "http_read"}) // nested under 1
	m.observe(id, lv, agentkit.TurnStart{Frame: 2})                           // a sub-agent turn — must not reset
	m.observe(id, lv, agentkit.ToolEnd{Frame: 1, ID: 2, Result: "r2"})
	m.observe(id, lv, agentkit.TurnEnd{Frame: 2}) // sub-agent turn ends — must not close the top turn
	m.observe(id, lv, agentkit.ToolEnd{Frame: 0, ID: 1, Result: "r1"})

	if lv.forest == nil {
		t.Fatal("a sub-agent TurnEnd (non-zero frame) wrongly closed the top-level turn")
	}

	m.observe(id, lv, agentkit.TurnEnd{Frame: 0}) // now the real close

	groups, err := st.LoadTools(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if len(g) != 2 {
		t.Fatalf("group = %+v, want 2 nodes", g)
	}
	if g[0].ID != 1 || g[0].Parent != 0 {
		t.Errorf("node 0 = %+v, want id 1 parent 0 (top level)", g[0])
	}
	if g[1].ID != 2 || g[1].Parent != 1 {
		t.Errorf("node 1 = %+v, want id 2 parent 1 (nested)", g[1])
	}
}
