package chat_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/app/chat"
)

// pastIdleTTL comfortably exceeds the manager's (unexported) idleTTL of 10m, so a synctest clock jump
// of this length crosses the reaper's one-minute tick past the TTL. Mirror it if idleTTL changes.
const pastIdleTTL = 12 * time.Minute

// Cancel is turn-scoped: it aborts the running turn but leaves the session open, so the next Submit
// runs a fresh turn. The first turn parks on a blocking tool, Cancel trips it, then a second turn
// (tool no longer blocking) completes cleanly on the SAME session.
func TestManager_Cancel_TurnScoped_SessionStays(t *testing.T) {
	release := make(chan struct{})
	rt := toolRuntime(t, toolThenAnswer("probe"), blockingTool(t, "probe", release))
	m, _ := newManagerRT(t, rt)
	defer m.CloseAll()

	var mu sync.Mutex
	var starts int
	var ends []error // TurnEnd errors, in order (frame 0 only)
	m.OnEvent(func(_ string, ev agentkit.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case agentkit.TurnStart:
			if e.Frame == 0 {
				starts++
			}
		case agentkit.TurnEnd:
			if e.Frame == 0 {
				ends = append(ends, e.Err)
			}
		}
	})

	id := "cafe01"
	m.Submit(id, "one")
	// Wait for TurnStart, not merely Inflight.Running (which the recorded input flips on before the
	// turn's cancel func is installed) — otherwise Cancel could race in as a no-op.
	if !eventually(func() bool { mu.Lock(); defer mu.Unlock(); return starts == 1 }) {
		t.Fatal("first turn never started")
	}

	m.Cancel(id)
	if !eventually(func() bool { mu.Lock(); defer mu.Unlock(); return len(ends) == 1 }) {
		t.Fatal("cancelled turn never ended")
	}
	mu.Lock()
	if ends[0] == nil {
		t.Error("cancelled turn ended with nil error, want a cancellation error")
	}
	mu.Unlock()

	// Session stays: a second turn on the same id runs to completion (tool no longer blocks).
	close(release)
	m.Submit(id, "two")
	if !eventually(func() bool { mu.Lock(); defer mu.Unlock(); return len(ends) == 2 && ends[1] == nil }) {
		t.Fatal("session did not survive the cancel — second turn did not complete cleanly")
	}
}

func TestManager_Cancel_UnknownID_NoOp(t *testing.T) {
	m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
	defer m.CloseAll()
	m.Cancel("deadbe") // no live session for this id — must not panic
}

// Open on an unknown id loads from the store and returns ONE shared live session; a second Open never
// duplicates it.
func TestManager_Open_UnknownID_LoadsFromStoreNotDuplicate(t *testing.T) {
	m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
	defer m.CloseAll()

	s1 := m.Open("abcdef")
	if s1 == nil {
		t.Fatal("Open returned nil session")
	}
	if s2 := m.Open("abcdef"); s2 != s1 {
		t.Fatal("second Open must return the SAME live session (single pump), not a duplicate")
	}
}

// Submit records the input as the in-flight turn's user message immediately — a client reopening
// before the turn ends sees Running+Input at once (the transcript only gets it at TurnEnd).
func TestManager_Submit_RecordsInflightInput_BeforeTurnEnd(t *testing.T) {
	release := make(chan struct{})
	m, _ := newManagerRT(t, runtime.New(blockingLLM(release)))
	defer m.CloseAll()
	defer close(release)

	id := "abc0de"
	m.Submit(id, "hello there")

	fl := m.Inflight(id)
	if !fl.Running || fl.Input != "hello there" {
		t.Fatalf("Inflight = %+v, want Running with Input %q recorded before the turn ends", fl, "hello there")
	}
}

func TestManager_Submit_OpensIfNeeded(t *testing.T) {
	m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
	defer m.CloseAll()

	id := "beef01"
	m.Submit(id, "hi")
	if !eventually(func() bool { msgs, _ := m.Transcript(id); return len(msgs) >= 2 }) {
		t.Fatal("Submit did not open the session and run the turn")
	}
}

// A client reopening mid-turn is handed the RUNNING turn: its own input plus the raw events streamed
// so far (partial reasoning/answer + the still-open tool call), which the client replays through the
// same fold as the live stream — no server-side render model.
func TestManager_ReopenMidTurn_HandedRunningTurn(t *testing.T) {
	release := make(chan struct{})
	llm := funcLLM{next: func(ctx context.Context, conv []agentkit.Message) (agentkit.Step, error) {
		if conv[len(conv)-1].Role == agentkit.RoleTool {
			return agentkit.Step{Answer: "final"}, nil
		}
		agentkit.Emit(ctx, agentkit.Thinking{Text: "pondering"})
		agentkit.Emit(ctx, agentkit.Token{Text: "partial"})
		return agentkit.Step{ToolCalls: []agentkit.ToolCall{{ID: "c1", Tool: "probe", Args: "{}"}}}, nil
	}}
	m, _ := newManagerRT(t, toolRuntime(t, llm, blockingTool(t, "probe", release)))
	defer m.CloseAll()
	defer close(release)

	id := "d00d01"
	m.Submit(id, "the question")

	// Wait until the buffered events carry the partial reasoning + answer + the (open) tool start.
	if !eventually(func() bool {
		fl := m.Inflight(id)
		return hasThinking(fl.Events, "pondering") && hasToken(fl.Events, "partial") && toolOpen(fl.Events)
	}) {
		t.Fatalf("in-flight turn not handed over: %+v", m.Inflight(id))
	}
	fl := m.Inflight(id)
	if !fl.Running || fl.Input != "the question" {
		t.Fatalf("Inflight = %+v, want Running with the caller's own input", fl)
	}
}

func hasToken(events []agentkit.Event, text string) bool {
	for _, e := range events {
		if t, ok := e.(agentkit.Token); ok && t.Text == text {
			return true
		}
	}
	return false
}

func hasThinking(events []agentkit.Event, text string) bool {
	for _, e := range events {
		if t, ok := e.(agentkit.Thinking); ok && t.Text == text {
			return true
		}
	}
	return false
}

// toolOpen reports whether the buffer has a ToolStart with no matching ToolEnd — a still-running call.
func toolOpen(events []agentkit.Event) bool {
	ended := map[uint64]bool{}
	for _, e := range events {
		if te, ok := e.(agentkit.ToolEnd); ok {
			ended[te.ID] = true
		}
	}
	for _, e := range events {
		if ts, ok := e.(agentkit.ToolStart); ok && !ended[ts.ID] {
			return true
		}
	}
	return false
}

// After a turn ends, the in-flight state is cleared (TurnEnd frame 0) — Inflight is the zero value.
func TestManager_Inflight_ZeroWhenIdle(t *testing.T) {
	m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
	defer m.CloseAll()

	id, _ := m.Start("hi")
	if !eventually(func() bool {
		msgs, _ := m.Transcript(id)
		return len(msgs) >= 2 && zeroInflight(m.Inflight(id))
	}) {
		t.Fatalf("Inflight after an idle turn = %+v, want the zero value", m.Inflight(id))
	}
}

func TestManager_Inflight_UnknownOrReaped_Zero(t *testing.T) {
	m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
	defer m.CloseAll()
	if fl := m.Inflight("nolive"); !zeroInflight(fl) {
		t.Fatalf("Inflight for an unknown id = %+v, want zero", fl)
	}
}

// The reaper unloads a session that has been idle past idleTTL; the next Open reloads a FRESH session
// from the store.
func TestManager_ReapIdle_UnloadsIdleSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
		defer m.CloseAll()

		id, first := m.Start("hi")
		synctest.Wait() // turn completes; idleSince stamped at TurnEnd

		time.Sleep(pastIdleTTL) // reaper ticks every minute; crosses the TTL
		synctest.Wait()

		if again := m.Open(id); again == first {
			t.Fatal("idle session was not reaped — Open returned the same session, not a fresh reload")
		}
	})
}

// A session with a RUNNING turn is never reaped, no matter how long the turn runs.
func TestManager_ReapIdle_NeverReapsRunningTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		m, _ := newManagerRT(t, toolRuntime(t, toolThenAnswer("probe"), blockingTool(t, "probe", release)))
		defer m.CloseAll()

		id := "aaaa01"
		first := m.Open(id)
		m.Submit(id, "hi")
		synctest.Wait() // turn is parked on the blocking tool → running

		time.Sleep(pastIdleTTL)
		synctest.Wait()

		if again := m.Open(id); again != first {
			t.Fatal("a running turn was reaped — Open returned a different session")
		}
		close(release) // let the turn finish so the bubble can drain
	})
}

// Reloading a chat after it was reaped APPENDS a new forest group; it never rewrites the earlier ones.
func TestManager_ReloadAfterReap_AppendsNewForestGroup_NeverRewrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _ := newManagerRT(t, toolRuntime(t, toolThenAnswer("probe"), okTool(t, "probe")))
		defer m.CloseAll()

		id := "bbbb01"
		m.Submit(id, "one")
		synctest.Wait() // turn 1 completes: transcript + forest group 1 persisted

		time.Sleep(pastIdleTTL)
		synctest.Wait() // reaped

		m.Submit(id, "two") // reloads from store, runs turn 2
		synctest.Wait()

		groups, err := m.Tools(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 2 {
			t.Fatalf("forest groups = %d, want 2 (turn 1's group preserved, turn 2's appended)", len(groups))
		}
		for i, g := range groups {
			if len(g) != 1 || g[0].Tool != "probe" {
				t.Fatalf("group %d = %+v, want one probe call", i, g)
			}
		}
	})
}

// CloseAll is idempotent (a second call must not double-close m.stop and panic) and it waits for the
// pumps to drain.
func TestManager_CloseAll_Idempotent_AndWaitsForPumps(t *testing.T) {
	m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
	m.Start("hi")
	m.CloseAll()
	m.CloseAll() // must be a no-op, not a panic on the already-closed stop channel
}

// CloseAll stops the reaper: after it returns, no bubble goroutine is left running (synctest would
// panic on a lingering reaper otherwise).
func TestManager_CloseAll_StopsReaper(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
		m.Start("hi")
		synctest.Wait()
		m.CloseAll()
		synctest.Wait() // reaper (and every pump) must have exited; else the bubble deadlocks
	})
}

// Delete stops the live session and removes the transcript from the store.
func TestManager_Delete_StopsSessionAndRemovesTranscript(t *testing.T) {
	m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
	defer m.CloseAll()

	id, _ := m.Start("hi")
	if !eventually(func() bool { msgs, _ := m.Transcript(id); return len(msgs) >= 2 }) {
		t.Fatal("turn did not persist before Delete")
	}
	if err := m.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if msgs, _ := m.Transcript(id); msgs != nil {
		t.Fatalf("transcript after Delete = %v, want nil (removed)", msgs)
	}
	if fl := m.Inflight(id); !zeroInflight(fl) {
		t.Fatalf("Inflight after Delete = %+v, want zero (session stopped)", fl)
	}
}

func TestManager_Delete_UnknownID_DelegatesToStore(t *testing.T) {
	m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
	defer m.CloseAll()
	if err := m.Delete("abcdef"); err != nil { // unknown but valid id → store's Delete is a no-op
		t.Fatalf("Delete of unknown chat = %v, want nil", err)
	}
}

// Start mints a valid chat id and submits the first message.
func TestManager_Start_MintsIDAndSubmits(t *testing.T) {
	m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
	defer m.CloseAll()

	id, sess := m.Start("hello")
	if !chat.ValidID(id) {
		t.Fatalf("Start minted id %q, which is not a valid chat id", id)
	}
	if sess == nil {
		t.Fatal("Start returned a nil session")
	}
	if !eventually(func() bool { msgs, _ := m.Transcript(id); return len(msgs) >= 2 }) {
		t.Fatal("Start did not submit the first message")
	}
}

// The emit sink is captured once, at pump start (read-once): a live pump keeps the callback it
// snapshotted, so a later OnEvent does not rewire an already-running session's stream.
func TestManager_OnEvent_CapturedAtPumpStart(t *testing.T) {
	m, _ := newManagerRT(t, runtime.New(fakeLLM{}))
	defer m.CloseAll()

	var mu sync.Mutex
	var first, second int
	m.OnEvent(func(_ string, ev agentkit.Event) { // set BEFORE Open → this pump captures cb1
		if _, ok := ev.(agentkit.TurnEnd); ok {
			mu.Lock()
			first++
			mu.Unlock()
		}
	})

	id := "aaa111"
	m.Submit(id, "hi")
	// Once cb1 has seen a TurnEnd, the pump has provably passed its capture point.
	if !eventually(func() bool { mu.Lock(); defer mu.Unlock(); return first == 1 }) {
		t.Fatal("callback set before Open never received events")
	}

	// Rewire the sink. The already-running pump captured cb1, so its stream must keep going to cb1.
	m.OnEvent(func(_ string, ev agentkit.Event) {
		if _, ok := ev.(agentkit.TurnEnd); ok {
			mu.Lock()
			second++
			mu.Unlock()
		}
	})
	m.Submit(id, "again")
	if !eventually(func() bool { mu.Lock(); defer mu.Unlock(); return first == 2 }) {
		t.Fatal("running pump stopped delivering to its captured callback after a later OnEvent")
	}
	mu.Lock()
	got2 := second
	mu.Unlock()
	if got2 != 0 {
		t.Errorf("late-registered callback received %d events from a running pump, want 0 (read-once at start)", got2)
	}
}

func TestManager_Cancel_ThenReap_RunningClearedAllowsReap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		m, _ := newManagerRT(t, toolRuntime(t, toolThenAnswer("probe"), blockingTool(t, "probe", release)))
		defer m.CloseAll()

		id := "cccc01"
		first := m.Open(id)
		m.Submit(id, "hi")
		synctest.Wait() // parked on the blocking tool → running, not reapable

		m.Cancel(id) // aborts the turn; the tool exits on ctx.Done, running clears, idleSince stamps
		synctest.Wait()

		time.Sleep(pastIdleTTL)
		synctest.Wait()

		if again := m.Open(id); again == first {
			t.Fatal("after Cancel cleared the running turn, the idle session should be reapable")
		}
	})
}

func TestNewID_ValidHexAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := chat.NewID()
		if !chat.ValidID(id) {
			t.Fatalf("NewID minted %q, not a valid chat id", id)
		}
		if len(id) != 12 { // 6 random bytes → 12 hex chars
			t.Fatalf("NewID minted %q (len %d), want 12 hex chars", id, len(id))
		}
		if seen[id] {
			t.Fatalf("NewID collided on %q", id)
		}
		seen[id] = true
	}
}
