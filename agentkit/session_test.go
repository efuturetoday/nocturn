package agentkit_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
)

// --- Once: the synchronous single-turn primitive ---

func TestOnce_FinalAnswer_NoTools(t *testing.T) {
	llm := &stepLLM{steps: []agentkit.Step{answerStep("hi")}}
	store := &fakeStore{}
	got, err := agentkit.Once(context.Background(), llm, "in", agentkit.WithStore(store, "s"))
	if err != nil {
		t.Fatalf("Once err = %v", err)
	}
	if got != "hi" {
		t.Fatalf("answer = %q, want hi", got)
	}
	h := store.history()
	if len(h) != 2 || h[0].Role != agentkit.RoleUser || h[0].Content != "in" ||
		h[1].Role != agentkit.RoleAssistant || h[1].Content != "hi" {
		t.Fatalf("history = %+v, want [{user in} {assistant hi}]", h)
	}
}

func TestOnce_ToolCallRoundTrip(t *testing.T) {
	set := newSet(t, echoTool(t, "echo", "R"))
	llm := &stepLLM{steps: []agentkit.Step{callStep("c1", "echo", "{}"), answerStep("done")}}
	store := &fakeStore{}
	got, err := agentkit.Once(context.Background(), llm, "in", agentkit.WithTools(set), agentkit.WithStore(store, "s"))
	if err != nil {
		t.Fatalf("Once err = %v", err)
	}
	if got != "done" {
		t.Fatalf("answer = %q, want done", got)
	}
	h := store.history()
	wantRoles := []agentkit.Role{agentkit.RoleUser, agentkit.RoleAssistant, agentkit.RoleTool, agentkit.RoleAssistant}
	if len(h) != len(wantRoles) {
		t.Fatalf("history len = %d, want %d: %+v", len(h), len(wantRoles), h)
	}
	for i, r := range wantRoles {
		if h[i].Role != r {
			t.Fatalf("history[%d].Role = %q, want %q", i, h[i].Role, r)
		}
	}
	if len(h[1].ToolCalls) != 1 || h[1].ToolCalls[0].ID != "c1" {
		t.Fatalf("assistant tool call = %+v, want id c1", h[1].ToolCalls)
	}
	if h[2].ToolCallID != "c1" || h[2].Content != "R" {
		t.Fatalf("tool result = %+v, want {toolCallID c1, R}", h[2])
	}
}

func TestOnce_ParallelToolExecution(t *testing.T) {
	const n = 3
	arrived := make(chan int, n)
	proceed := make(chan struct{})
	mk := func(name string, idx int) agentkit.Tool {
		return newTool(t, name, func(ctx context.Context, _ string) (string, error) {
			arrived <- idx
			<-proceed
			return "r" + string(rune('0'+idx)), nil
		})
	}
	set := newSet(t, mk("t0", 0), mk("t1", 1), mk("t2", 2))
	llm := &stepLLM{steps: []agentkit.Step{
		{ToolCalls: []agentkit.ToolCall{
			{ID: "a", Tool: "t0", Args: "{}"},
			{ID: "b", Tool: "t1", Args: "{}"},
			{ID: "c", Tool: "t2", Args: "{}"},
		}},
		answerStep("done"),
	}}
	store := &fakeStore{}

	errc := make(chan error, 1)
	go func() {
		_, err := agentkit.Once(context.Background(), llm, "go", agentkit.WithTools(set), agentkit.WithStore(store, "s"))
		errc <- err
	}()

	// All three must ARRIVE before any is allowed to proceed — impossible under serial execution
	// (the first would block on proceed and the second read would hang), so this proves concurrency.
	for range n {
		<-arrived
	}
	close(proceed)
	if err := <-errc; err != nil {
		t.Fatalf("Once err = %v", err)
	}

	// Results are index-aligned to call order (a,b,c → r0,r1,r2), not completion order.
	h := store.history()
	var results []agentkit.Message
	for _, m := range h {
		if m.Role == agentkit.RoleTool {
			results = append(results, m)
		}
	}
	wantID := []string{"a", "b", "c"}
	wantContent := []string{"r0", "r1", "r2"}
	if len(results) != n {
		t.Fatalf("tool results = %d, want %d", len(results), n)
	}
	for i, m := range results {
		if m.ToolCallID != wantID[i] || m.Content != wantContent[i] {
			t.Fatalf("result[%d] = {%q %q}, want {%q %q}", i, m.ToolCallID, m.Content, wantID[i], wantContent[i])
		}
	}
}

func TestOnce_MaxStepsStop(t *testing.T) {
	set := newSet(t, echoTool(t, "echo", "R"))
	llm := &stepLLM{steps: []agentkit.Step{callStep("c", "echo", "{}")}} // always a tool call
	_, err := agentkit.Once(context.Background(), llm, "go",
		agentkit.WithTools(set), agentkit.WithMaxSteps(2))
	if !errors.Is(err, agentkit.ErrMaxSteps) {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
	if n := llm.callCount(); n != 2 {
		t.Fatalf("Next calls = %d, want 2", n)
	}
}

func TestOnce_ToolErrorFedBackNonFatal(t *testing.T) {
	boom := newTool(t, "boom", func(context.Context, string) (string, error) {
		return "", errors.New("boom")
	})
	set := newSet(t, boom)
	llm := &stepLLM{steps: []agentkit.Step{callStep("c1", "boom", "{}"), answerStep("recovered")}}
	store := &fakeStore{}
	got, err := agentkit.Once(context.Background(), llm, "go",
		agentkit.WithTools(set), agentkit.WithStore(store, "s"))
	if err != nil {
		t.Fatalf("err = %v, want nil (tool error is non-fatal)", err)
	}
	if got != "recovered" {
		t.Fatalf("answer = %q, want recovered", got)
	}
	if tr := toolResult(t, store.history()); tr.Content != "error: boom" {
		t.Fatalf("tool result content = %q, want %q", tr.Content, "error: boom")
	}
}

func TestOnce_UnknownToolNonFatal(t *testing.T) {
	set := newSet(t, echoTool(t, "known", "R"))
	llm := &stepLLM{steps: []agentkit.Step{callStep("c1", "x", "{}"), answerStep("moved on")}}
	store := &fakeStore{}
	got, err := agentkit.Once(context.Background(), llm, "go",
		agentkit.WithTools(set), agentkit.WithStore(store, "s"))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != "moved on" {
		t.Fatalf("answer = %q, want moved on", got)
	}
	// runTools prefixes tool errors with "error: "; the underlying message names the unknown tool.
	if c := toolResult(t, store.history()).Content; !strings.Contains(c, `agentkit: unknown tool "x"`) {
		t.Fatalf("tool result = %q, want it to mention the unknown tool", c)
	}
}

func TestOnce_TokenLimitStop_ProviderUsage(t *testing.T) {
	set := newSet(t, echoTool(t, "echo", "R"))
	llm := &stepLLM{steps: []agentkit.Step{
		callStepT("c1", "echo", "{}", tc(0, 0, 60)),
		callStepT("c2", "echo", "{}", tc(0, 0, 60)),
	}}
	_, err := agentkit.Once(context.Background(), llm, "go",
		agentkit.WithTools(set), agentkit.WithTokenLimit(100))
	if !errors.Is(err, agentkit.ErrTokenLimit) {
		t.Fatalf("err = %v, want ErrTokenLimit", err)
	}
	if n := llm.callCount(); n != 2 {
		t.Fatalf("Next calls = %d, want 2 (no third round-trip)", n)
	}
}

func TestOnce_TokenLimitStop_OnFinalAnswer(t *testing.T) {
	llm := &stepLLM{steps: []agentkit.Step{answerStepT("final", tc(0, 0, 80))}}
	got, err := agentkit.Once(context.Background(), llm, "go", agentkit.WithTokenLimit(50))
	if !errors.Is(err, agentkit.ErrTokenLimit) {
		t.Fatalf("err = %v, want ErrTokenLimit", err)
	}
	if got != "final" {
		t.Fatalf("answer = %q, want final (answer still delivered)", got)
	}
}

func TestOnce_TokenizerFallback_WhenProviderReportsZero(t *testing.T) {
	set := newSet(t, echoTool(t, "echo", "R"))

	t.Run("fallback estimates when provider reports zero", func(t *testing.T) {
		cs := &captureSink{}
		ctx := agentkit.WithSink(context.Background(), cs.fn())
		spy := &spyTokenizer{per: 80} // large estimate → trips the 100 limit after the final step
		llm := &stepLLM{steps: []agentkit.Step{answerStep("done")}}
		_, err := agentkit.Once(ctx, llm, "go",
			agentkit.WithTools(set), agentkit.WithTokenLimit(100), agentkit.WithTokenizer(spy))
		if !errors.Is(err, agentkit.ErrTokenLimit) {
			t.Fatalf("err = %v, want ErrTokenLimit from estimate", err)
		}
		if spy.callCount() == 0 {
			t.Fatal("tokenizer was not consulted despite zero provider usage")
		}
		if te := lastTurnEnd(t, cs.all()); te.Tokens.Total == 0 {
			t.Fatal("TurnEnd.Tokens.Total = 0, want the estimate")
		}
	})

	t.Run("tokenizer not consulted when provider usage present", func(t *testing.T) {
		spy := &spyTokenizer{per: 80}
		llm := &stepLLM{steps: []agentkit.Step{answerStepT("done", tc(1, 1, 2))}}
		if _, err := agentkit.Once(context.Background(), llm, "go",
			agentkit.WithTools(set), agentkit.WithTokenLimit(1000), agentkit.WithTokenizer(spy)); err != nil {
			t.Fatalf("err = %v", err)
		}
		if n := spy.callCount(); n != 0 {
			t.Fatalf("tokenizer consulted %d times, want 0 (provider usage present)", n)
		}
	})
}

func TestOnce_HistoryAndProducedAccumulation(t *testing.T) {
	set := newSet(t, echoTool(t, "echo", "R"))
	llm := &stepLLM{steps: []agentkit.Step{callStep("c1", "echo", "{}"), answerStep("done")}}
	store := &fakeStore{}
	if _, err := agentkit.Once(context.Background(), llm, "in",
		agentkit.WithTools(set), agentkit.WithStore(store, "s")); err != nil {
		t.Fatalf("Once err = %v", err)
	}
	// produced order: assistant(tool call) → tool result → final assistant.
	h := store.history()
	wantRoles := []agentkit.Role{agentkit.RoleUser, agentkit.RoleAssistant, agentkit.RoleTool, agentkit.RoleAssistant}
	if len(h) != len(wantRoles) {
		t.Fatalf("history = %+v", h)
	}
	for i, r := range wantRoles {
		if h[i].Role != r {
			t.Fatalf("history[%d].Role = %q, want %q", i, h[i].Role, r)
		}
	}
	// The loop feeds the tool result back: the second Next sees user + assistant(tc) + tool result.
	conv2 := llm.convAt(1)
	if len(conv2) < 3 || conv2[len(conv2)-1].Role != agentkit.RoleTool {
		t.Fatalf("second Next conv did not carry the fed-back tool result: %+v", conv2)
	}
}

func TestOnce_TokenTotalsAccumulateOnTurnEnd(t *testing.T) {
	set := newSet(t, echoTool(t, "echo", "R"))
	cs := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), cs.fn())
	llm := &stepLLM{steps: []agentkit.Step{
		callStepT("c1", "echo", "{}", tc(10, 5, 15)),
		answerStepT("done", tc(10, 5, 15)),
	}}
	if _, err := agentkit.Once(ctx, llm, "go", agentkit.WithTools(set)); err != nil {
		t.Fatalf("Once err = %v", err)
	}
	if got := lastTurnEnd(t, cs.all()).Tokens; got != tc(20, 10, 30) {
		t.Fatalf("TurnEnd.Tokens = %+v, want {20 10 30}", got)
	}
}

// --- Turn / Session (async NewSession path) ---

func TestTurn_TurnStartTurnEndBracket(t *testing.T) {
	llm := &stepLLM{steps: []agentkit.Step{answerStepT("hi", tc(1, 2, 3))}}
	s, sub := newSession(t, llm)
	s.Submit("go")
	end, events := nextTurnEnd(t, sub)
	if _, ok := events[0].(agentkit.TurnStart); !ok {
		t.Fatalf("first event = %T, want TurnStart", events[0])
	}
	if events[0].(agentkit.TurnStart).Frame != 0 {
		t.Fatalf("TurnStart.Frame = %d, want 0", events[0].(agentkit.TurnStart).Frame)
	}
	if end.Frame != 0 || end.Err != nil || end.Tokens != tc(1, 2, 3) {
		t.Fatalf("TurnEnd = %+v, want {Frame:0 Err:nil Tokens:{1 2 3}}", end)
	}
}

func TestTurn_PersistOncePerTurn(t *testing.T) {
	set := newSet(t, echoTool(t, "echo", "R"))
	store := &fakeStore{}
	// A multi-round turn (tool call then answer): Save must fire exactly once, not per round.
	llm := &stepLLM{steps: []agentkit.Step{callStep("c1", "echo", "{}"), answerStep("done")}}
	s, sub := newSession(t, llm, agentkit.WithTools(set), agentkit.WithStore(store, "s"))
	s.Submit("go")
	nextTurnEnd(t, sub)
	if n := store.saveCount(); n != 1 {
		t.Fatalf("Save called %d times, want 1 per turn", n)
	}
	if h := store.history(); len(h) != 4 {
		t.Fatalf("persisted history len = %d, want the full post-turn 4", len(h))
	}
}

func TestTurn_OneRoleUserPerTurnInvariant(t *testing.T) {
	const turns = 3
	store := &fakeStore{}
	llm := &stepLLM{steps: []agentkit.Step{answerStep("ok")}}
	s, sub := newSession(t, llm, agentkit.WithStore(store, "s"))
	for range turns {
		s.Submit("q")
		nextTurnEnd(t, sub)
	}
	h := store.history()
	var users int
	for i, m := range h {
		if m.Role == agentkit.RoleUser {
			users++
			if i+1 >= len(h) || h[i+1].Role != agentkit.RoleAssistant {
				t.Fatalf("RoleUser at %d not followed by an assistant message: %+v", i, h)
			}
		}
	}
	if users != turns {
		t.Fatalf("RoleUser count = %d, want %d (one per turn, not per round)", users, turns)
	}
}

func TestSession_SubmitSubscribe_Serialized(t *testing.T) {
	llm := &stepLLM{
		steps:   []agentkit.Step{answerStep("a")},
		entered: make(chan struct{}),
		gate:    make(chan struct{}),
	}
	s, sub := newSession(t, llm)

	s.Submit("one")
	s.Submit("two")

	<-llm.entered // turn 1 has entered Next and is parked on the gate
	// The single worker is stuck in turn 1's Next, so turn 2 cannot have started.
	select {
	case <-llm.entered:
		t.Fatal("turn 2 started before turn 1 finished")
	default:
	}

	llm.gate <- struct{}{} // release turn 1
	end1, _ := nextTurnEnd(t, sub)
	if end1.Err != nil {
		t.Fatalf("turn 1 TurnEnd.Err = %v", end1.Err)
	}

	<-llm.entered // only now, after turn 1 ended, does turn 2 enter Next
	llm.gate <- struct{}{}
	if end2, _ := nextTurnEnd(t, sub); end2.Err != nil {
		t.Fatalf("turn 2 TurnEnd.Err = %v", end2.Err)
	}
}

func TestSession_Cancel_MidTurn(t *testing.T) {
	entered := make(chan struct{}, 1)
	block := newTool(t, "block", func(ctx context.Context, _ string) (string, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	})
	set := newSet(t, block)
	store := &fakeStore{}
	// Turn 1's step is a tool call (blocks); turn 2's step (call #2) is a plain answer.
	llm := &stepLLM{steps: []agentkit.Step{callStep("c1", "block", "{}"), answerStep("normal")}}
	s, sub := newSession(t, llm, agentkit.WithTools(set), agentkit.WithStore(store, "s"))

	s.Submit("go")
	<-entered
	s.Cancel()

	end, _ := nextTurnEnd(t, sub)
	if !errors.Is(end.Err, context.Canceled) {
		t.Fatalf("TurnEnd.Err = %v, want context.Canceled", end.Err)
	}
	// Partial output is still appended + persisted: user + assistant(tool call) + tool result.
	if h := store.history(); len(h) < 3 || h[1].Role != agentkit.RoleAssistant || h[2].Role != agentkit.RoleTool {
		t.Fatalf("partial history not persisted: %+v", store.history())
	}

	// The next submit runs normally on a fresh turn context.
	s.Submit("again")
	if end2, _ := nextTurnEnd(t, sub); end2.Err != nil {
		t.Fatalf("turn 2 TurnEnd.Err = %v, want nil", end2.Err)
	}
}

func TestSession_Close_ClosesStreamAndStopsLoop(t *testing.T) {
	llm := &stepLLM{steps: []agentkit.Step{answerStep("hi")}}
	ctx := context.Background()
	s := agentkit.NewSession(ctx, llm)
	sub := s.Subscribe()

	s.Close()
	for range sub { // must terminate once Close cancels the loop
	}

	// A post-close Submit is a no-op (no block, no panic).
	done := make(chan struct{})
	go func() {
		s.Submit("late")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("post-close Submit blocked")
	}
	s.Close() // idempotent
}

func TestSession_ContextCancel_EquivalentToClose(t *testing.T) {
	llm := &stepLLM{steps: []agentkit.Step{answerStep("hi")}}
	ctx, cancel := context.WithCancel(context.Background())
	s := agentkit.NewSession(ctx, llm)
	sub := s.Subscribe()

	cancel()
	closed := make(chan struct{})
	go func() {
		for range sub {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx cancel did not close the subscribe stream")
	}
}

func TestTurn_ErrTurnTimeout_Surfaced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := newTool(t, "block", func(ctx context.Context, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
		set := newSet(t, block)
		llm := &stepLLM{steps: []agentkit.Step{callStep("c1", "block", "{}"), answerStep("never")}}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := agentkit.NewSession(ctx, llm, agentkit.WithTools(set), agentkit.WithTimeout(50*time.Millisecond))
		sub := s.Subscribe()
		s.Submit("go")

		end, _ := nextTurnEnd(t, sub)
		// A wall-clock stop cancels the ctx with cause ErrTurnTimeout; turn() surfaces that clear
		// reason rather than the bare context.Canceled the aborted tool call bubbled up.
		if !errors.Is(end.Err, agentkit.ErrTurnTimeout) {
			t.Fatalf("TurnEnd.Err = %v, want ErrTurnTimeout", end.Err)
		}
		cancel()
		for range sub {
		}
	})
}

func TestBuildSession_LoadsPersistedHistory(t *testing.T) {
	store := &fakeStore{loadMsgs: []agentkit.Message{
		{Role: agentkit.RoleUser, Content: "prior q"},
		{Role: agentkit.RoleAssistant, Content: "prior a"},
	}}
	llm := &stepLLM{steps: []agentkit.Step{answerStep("new")}}
	if _, err := agentkit.Once(context.Background(), llm, "next", agentkit.WithStore(store, "s")); err != nil {
		t.Fatalf("Once err = %v", err)
	}
	conv := llm.convAt(0)
	if len(conv) < 3 || conv[0].Content != "prior q" || conv[1].Content != "prior a" {
		t.Fatalf("first Next did not start from persisted history: %+v", conv)
	}
}

func TestBuildSession_LoadError_StartsEmpty(t *testing.T) {
	log := &memLogger{}
	store := &fakeStore{loadErr: errors.New("disk gone")}
	llm := &stepLLM{steps: []agentkit.Step{answerStep("ok")}}
	got, err := agentkit.Once(context.Background(), llm, "q",
		agentkit.WithStore(store, "s"), agentkit.WithLogger(log))
	if err != nil {
		t.Fatalf("Once err = %v, want a usable session despite load error", err)
	}
	if got != "ok" {
		t.Fatalf("answer = %q", got)
	}
	if conv := llm.convAt(0); len(conv) != 1 || conv[0].Content != "q" {
		t.Fatalf("history not empty after load error: %+v", conv)
	}
	if !log.hasWarn("load history failed") {
		t.Fatal("load error was not logged at warn")
	}
}

// --- Once edge cases ---

func TestOnce_ModelError_Fatal(t *testing.T) {
	llm := &stepLLM{err: errors.New("provider down")}
	store := &fakeStore{}
	got, err := agentkit.Once(context.Background(), llm, "q", agentkit.WithStore(store, "s"))
	if err == nil || !strings.Contains(err.Error(), "agentkit: model call:") {
		t.Fatalf("err = %v, want it wrapped with 'agentkit: model call:'", err)
	}
	if got != "" {
		t.Fatalf("answer = %q, want empty", got)
	}
	if h := store.history(); len(h) != 1 || h[0].Role != agentkit.RoleUser {
		t.Fatalf("history = %+v, want only the user row", h)
	}
}

func TestOnce_CtxAlreadyCancelled_ReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	llm := &stepLLM{steps: []agentkit.Step{answerStep("unreached")}}
	_, err := agentkit.Once(ctx, llm, "q")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := llm.callCount(); n != 0 {
		t.Fatalf("Next calls = %d, want 0 (ctx checked before each Next)", n)
	}
}

func TestOnce_EmptyAnswer(t *testing.T) {
	llm := &stepLLM{steps: []agentkit.Step{answerStep("")}}
	store := &fakeStore{}
	got, err := agentkit.Once(context.Background(), llm, "q", agentkit.WithStore(store, "s"))
	if err != nil || got != "" {
		t.Fatalf("Once = (%q, %v), want (\"\", nil)", got, err)
	}
	h := store.history()
	if len(h) != 2 || h[1].Role != agentkit.RoleAssistant || h[1].Content != "" {
		t.Fatalf("history = %+v, want a trailing empty assistant message", h)
	}
}

func TestOnce_MaxStepsZero_UsesDefault(t *testing.T) {
	const defaultMaxSteps = 16
	set := newSet(t, echoTool(t, "echo", "R"))
	llm := &stepLLM{steps: []agentkit.Step{callStep("c", "echo", "{}")}} // always tool calls
	_, err := agentkit.Once(context.Background(), llm, "go",
		agentkit.WithTools(set), agentkit.WithMaxSteps(0))
	if !errors.Is(err, agentkit.ErrMaxSteps) {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
	if n := llm.callCount(); n != defaultMaxSteps {
		t.Fatalf("Next calls = %d, want the default %d", n, defaultMaxSteps)
	}
}

func TestPersist_NoStore_NoOp(t *testing.T) {
	llm := &stepLLM{steps: []agentkit.Step{answerStep("ok")}}
	if _, err := agentkit.Once(context.Background(), llm, "q"); err != nil {
		t.Fatalf("Once without a store err = %v", err)
	}
}

func TestPersist_SaveError_Logged(t *testing.T) {
	log := &memLogger{}
	store := &fakeStore{saveErr: errors.New("disk full")}
	llm := &stepLLM{steps: []agentkit.Step{answerStep("ok")}}
	if _, err := agentkit.Once(context.Background(), llm, "q",
		agentkit.WithStore(store, "s"), agentkit.WithLogger(log)); err != nil {
		t.Fatalf("Once err = %v, want save error to be non-fatal", err)
	}
	if !log.hasWarn("persist failed") {
		t.Fatal("save error was not logged at warn")
	}
}

func TestSink_DoesNotBlockPastCancel(t *testing.T) {
	// A step with many concurrent tool calls floods the 64-slot out buffer while nobody drains it,
	// so the sink blocks on send. Cancelling the session must unblock it via <-ctx.Done(), letting
	// the loop exit and close the stream (otherwise this test hangs on the final drain).
	const calls = 100
	tcs := make([]agentkit.ToolCall, calls)
	tools := make([]agentkit.Tool, calls)
	for i := range calls {
		name := "t" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		tcs[i] = agentkit.ToolCall{ID: name, Tool: name, Args: "{}"}
		tools[i] = echoTool(t, name, "R")
	}
	set := newSet(t, tools...)
	llm := &stepLLM{steps: []agentkit.Step{{ToolCalls: tcs}, answerStep("done")}}

	ctx, cancel := context.WithCancel(context.Background())
	s := agentkit.NewSession(ctx, llm, agentkit.WithTools(set))
	sub := s.Subscribe()

	s.Submit("go")
	<-sub // consume TurnStart, then stop draining so the buffer fills and the sink parks

	cancel() // must release the parked sink

	closed := make(chan struct{})
	go func() {
		for range sub {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("sink stayed blocked past cancel; loop never closed the stream")
	}
}

func TestAssemble_SystemPromptEphemeral(t *testing.T) {
	llm := &stepLLM{steps: []agentkit.Step{answerStep("ok")}}
	store := &fakeStore{}
	if _, err := agentkit.Once(context.Background(), llm, "q",
		agentkit.WithSystem("SYSTEM"), agentkit.WithStore(store, "s")); err != nil {
		t.Fatalf("Once err = %v", err)
	}
	// The system prompt goes to the model...
	conv := llm.convAt(0)
	if len(conv) == 0 || conv[0].Role != agentkit.RoleSystem || !strings.Contains(conv[0].Content, "SYSTEM") {
		t.Fatalf("system prompt not sent to model: %+v", conv)
	}
	// ...but is never persisted.
	for _, m := range store.history() {
		if m.Role == agentkit.RoleSystem {
			t.Fatalf("system prompt leaked into persisted history: %+v", store.history())
		}
	}
}

// TestSystemFunc_WinsOverSystem: a provider function supersedes the static prompt, so a consumer can
// fold in context that only exists at turn time.
func TestSystemFunc_WinsOverSystem(t *testing.T) {
	llm := &stepLLM{steps: []agentkit.Step{answerStep("ok")}}
	if _, err := agentkit.Once(context.Background(), llm, "q",
		agentkit.WithSystem("STATIC"), agentkit.WithSystemFunc(func() string { return "DYNAMIC" })); err != nil {
		t.Fatalf("Once err = %v", err)
	}
	conv := llm.convAt(0)
	if len(conv) == 0 || conv[0].Role != agentkit.RoleSystem {
		t.Fatalf("no system message: %+v", conv)
	}
	if !strings.Contains(conv[0].Content, "DYNAMIC") || strings.Contains(conv[0].Content, "STATIC") {
		t.Fatalf("system prompt = %q, want the func's value to win", conv[0].Content)
	}
}

// TestSystemFunc_ReevaluatedPerTurn is the point of the option: a value that changes between turns
// must reach the model on the very next turn, not only in a fresh session.
func TestSystemFunc_ReevaluatedPerTurn(t *testing.T) {
	llm := &stepLLM{steps: []agentkit.Step{answerStep("one"), answerStep("two")}}
	prompt := "FIRST"
	sess := agentkit.NewSession(context.Background(), llm,
		agentkit.WithSystemFunc(func() string { return prompt }))
	defer sess.Close()
	sub := sess.Subscribe()

	awaitTurn := func() {
		t.Helper()
		for {
			select {
			case ev, ok := <-sub:
				if !ok {
					t.Fatal("event stream closed before TurnEnd")
				}
				if _, done := ev.(agentkit.TurnEnd); done {
					return
				}
			case <-time.After(3 * time.Second):
				t.Fatal("turn did not end")
			}
		}
	}
	sess.Submit("a")
	awaitTurn()
	prompt = "SECOND" // changed between turns, with the session still open
	sess.Submit("b")
	awaitTurn()

	if got := llm.convAt(0); len(got) == 0 || !strings.Contains(got[0].Content, "FIRST") {
		t.Fatalf("turn 1 system prompt = %+v, want FIRST", got)
	}
	if got := llm.convAt(1); len(got) == 0 || !strings.Contains(got[0].Content, "SECOND") {
		t.Fatalf("turn 2 system prompt = %+v, want SECOND", got)
	}
}

func TestToolset_SkillsAddsLoadSkillTool(t *testing.T) {
	set := newSet(t, echoTool(t, "echo", "R"))
	skills, err := agentkit.NewSkillSet(agentkit.Skill{Name: "pdf", Description: "handle pdfs", Body: "b"})
	if err != nil {
		t.Fatalf("NewSkillSet: %v", err)
	}

	t.Run("with skills merges skill_load", func(t *testing.T) {
		llm := &stepLLM{steps: []agentkit.Step{answerStep("ok")}}
		if _, err := agentkit.Once(context.Background(), llm, "q",
			agentkit.WithTools(set), agentkit.WithSkills(skills)); err != nil {
			t.Fatalf("Once err = %v", err)
		}
		if !hasSpec(llm.specsAt(0), "skill_load") {
			t.Fatalf("skill_load not offered to the model: %+v", llm.specsAt(0))
		}
	})

	t.Run("without skills no skill_load", func(t *testing.T) {
		llm := &stepLLM{steps: []agentkit.Step{answerStep("ok")}}
		if _, err := agentkit.Once(context.Background(), llm, "q", agentkit.WithTools(set)); err != nil {
			t.Fatalf("Once err = %v", err)
		}
		if hasSpec(llm.specsAt(0), "skill_load") {
			t.Fatalf("skill_load offered without any skills: %+v", llm.specsAt(0))
		}
	})
}

func TestEstimate_TokenizerError_CountsZero(t *testing.T) {
	cs := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), cs.fn())
	log := &memLogger{}
	spy := &spyTokenizer{err: errors.New("tokenizer boom")}
	llm := &stepLLM{steps: []agentkit.Step{answerStep("some answer text")}} // provider reports 0 usage
	if _, err := agentkit.Once(ctx, llm, "q",
		agentkit.WithTokenizer(spy), agentkit.WithLogger(log)); err != nil {
		t.Fatalf("Once err = %v, want tokenizer error to be non-fatal", err)
	}
	if got := lastTurnEnd(t, cs.all()).Tokens.Total; got != 0 {
		t.Fatalf("estimated tokens = %d, want 0 when the tokenizer errors", got)
	}
	if !log.hasWarn("tokenizer count failed") {
		t.Fatal("tokenizer error was not logged at warn")
	}
}

// --- local helpers ---

func toolResult(t *testing.T, h []agentkit.Message) agentkit.Message {
	t.Helper()
	for _, m := range h {
		if m.Role == agentkit.RoleTool {
			return m
		}
	}
	t.Fatalf("no tool-result message in history: %+v", h)
	return agentkit.Message{}
}

func hasSpec(specs []agentkit.ToolSpec, name string) bool {
	for _, s := range specs {
		if s.Name == name {
			return true
		}
	}
	return false
}

func TestCore_NoHITL_WithoutGate(t *testing.T) {
	// A deny-worthy action runs FREE when no gate machinery is installed: the agentkit core has no
	// notion of approval, so a tool simply executes. (Gating is a separate, opt-in layer.)
	var ran bool
	tool := newTool(t, "act", func(context.Context, string) (string, error) {
		ran = true
		return "did it", nil
	})
	set := newSet(t, tool)
	llm := &stepLLM{steps: []agentkit.Step{callStep("a", "act", "{}"), answerStep("done")}}
	if _, err := agentkit.Once(context.Background(), llm, "go", agentkit.WithTools(set)); err != nil {
		t.Fatalf("Once err = %v", err)
	}
	if !ran {
		t.Fatal("tool did not run; core should be HITL-agnostic without a gate")
	}
}
