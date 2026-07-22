package runtime_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
)

// --- fakes -------------------------------------------------------------------

// scriptedLLM is a fake agentkit.LLM that returns a fixed sequence of steps and captures the
// conversation and tool specs it is handed on each Next — so a test can assert what the runtime
// wired into the turn. Reads happen after the turn completes; the mutex keeps it -race clean.
type scriptedLLM struct {
	steps []agentkit.Step

	mu    sync.Mutex
	calls int
	convs [][]agentkit.Message
	specs [][]agentkit.ToolSpec
}

var _ agentkit.LLM = (*scriptedLLM)(nil)

func (l *scriptedLLM) Next(_ context.Context, conv []agentkit.Message, tools []agentkit.ToolSpec) (agentkit.Step, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := l.calls
	l.calls++
	l.convs = append(l.convs, append([]agentkit.Message(nil), conv...))
	l.specs = append(l.specs, append([]agentkit.ToolSpec(nil), tools...))
	if i < len(l.steps) {
		return l.steps[i], nil
	}
	return agentkit.Step{Answer: "done"}, nil // terminate any overrun turn
}

func (l *scriptedLLM) convAt(i int) []agentkit.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.convs[i]
}

func (l *scriptedLLM) specsAt(i int) []agentkit.ToolSpec {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.specs[i]
}

// lastToolResult returns the content of the most recent RoleTool message across all captured
// conversations — the tool-result the runtime fed back to the model (a Deny surfaces here).
func (l *scriptedLLM) lastToolResult(t *testing.T) string {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.convs) - 1; i >= 0; i-- {
		for j := len(l.convs[i]) - 1; j >= 0; j-- {
			if l.convs[i][j].Role == agentkit.RoleTool {
				return l.convs[i][j].Content
			}
		}
	}
	t.Fatal("no tool-result message captured")
	return ""
}

func callStep(id, tool, args string) agentkit.Step {
	return agentkit.Step{ToolCalls: []agentkit.ToolCall{{ID: id, Tool: tool, Args: args}}}
}

func answerStep(a string) agentkit.Step { return agentkit.Step{Answer: a} }

// toolRec records whether a fake tool ran (ran) and, for a self-gating tool, whether it got past its
// own gate.Check to do its work (work).
type toolRec struct {
	ran  atomic.Bool
	work atomic.Bool
}

// newRecordingTool builds a plain tool that records it ran and returns out.
func newRecordingTool(t *testing.T, name, out string) (agentkit.Tool, *toolRec) {
	t.Helper()
	rec := &toolRec{}
	tool, err := agentkit.NewTool(name, "recording test tool",
		func(context.Context, string) (string, error) {
			rec.ran.Store(true)
			return out, nil
		})
	if err != nil {
		t.Fatalf("NewTool(%q): %v", name, err)
	}
	return tool, rec
}

// newSelfGatingTool builds a tool that gates ITSELF on axis via gate.Check (like a host allowlist),
// independent of the name-based Wrap. It only sees the permission machinery if it was installed on
// the ctx — so it isolates the "gate installed on ctx" behavior from the tool-name wrapper.
func newSelfGatingTool(t *testing.T, name, axis, out string) (agentkit.Tool, *toolRec) {
	t.Helper()
	rec := &toolRec{}
	tool, err := agentkit.NewTool(name, "self-gating test tool",
		func(ctx context.Context, _ string) (string, error) {
			if err := gate.Check(ctx, gate.Action{Kind: axis}, nil); err != nil {
				return "", err
			}
			rec.work.Store(true)
			return out, nil
		})
	if err != nil {
		t.Fatalf("NewTool(%q): %v", name, err)
	}
	return tool, rec
}

// fakeApprover is a scripted gate.Approver that records how often it was asked.
type fakeApprover struct {
	approve bool
	recall  gate.Recall

	mu    sync.Mutex
	calls int
}

func (f *fakeApprover) Ask(_ context.Context, a gate.Action, _ []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.approve, gate.Grant{Kind: a.Kind, Target: a.Target}, f.recall, nil
}

func (f *fakeApprover) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func toolset(t *testing.T, tools ...agentkit.Tool) agentkit.ToolSet {
	t.Helper()
	ts, err := agentkit.NewToolSet(tools...)
	if err != nil {
		t.Fatalf("NewToolSet: %v", err)
	}
	return ts
}

// --- New: gating wiring ------------------------------------------------------

// TestNew_GateWrapsTools_WhenPolicySet pins that a configured Policy makes New WrapAll the tools
// (so a Deny surfaces as the tool result and the underlying tool never runs) and that a nil Grants
// defaults to an in-memory store that actually remembers an approval across calls.
func TestNew_GateWrapsTools_WhenPolicySet(t *testing.T) {
	t.Parallel()

	t.Run("wraps tools and surfaces Deny", func(t *testing.T) {
		t.Parallel()
		tool, rec := newRecordingTool(t, "danger", "DID-RUN")
		policy := gate.PolicyFunc(func(a gate.Action) gate.Ruling {
			if a.Kind == "danger" {
				return gate.Denied()
			}
			return gate.Allowed()
		})
		llm := &scriptedLLM{steps: []agentkit.Step{callStep("c1", "danger", "{}"), answerStep("ok")}}
		rt := runtime.New(llm,
			runtime.WithTools(toolset(t, tool)),
			runtime.WithGate(policy, nil, nil),
		)

		answer, err := rt.Once(context.Background(), "go")
		if err != nil {
			t.Fatalf("Once: unexpected error %v", err)
		}
		if answer != "ok" {
			t.Errorf("answer = %q, want %q", answer, "ok")
		}
		if got := llm.lastToolResult(t); !strings.Contains(got, "gate: denied") {
			t.Errorf("tool result = %q, want it to carry the gate denial", got)
		}
		if rec.ran.Load() {
			t.Error("wrapped tool ran despite a Deny policy — WrapAll not applied")
		}
	})

	t.Run("nil Grants defaults to a remembering store", func(t *testing.T) {
		t.Parallel()
		tool, rec := newRecordingTool(t, "notify", "sent")
		policy := gate.PolicyFunc(func(a gate.Action) gate.Ruling {
			if a.Kind == "notify" {
				return gate.AskWith(gate.RecallSession)
			}
			return gate.Allowed()
		})
		appr := &fakeApprover{approve: true, recall: gate.RecallSession}
		llm := &scriptedLLM{steps: []agentkit.Step{
			callStep("c1", "notify", "{}"),
			callStep("c2", "notify", "{}"),
			answerStep("ok"),
		}}
		rt := runtime.New(llm,
			runtime.WithTools(toolset(t, tool)),
			runtime.WithGate(policy, nil, appr), // nil Grants → default MemGrants
		)

		if _, err := rt.Once(context.Background(), "go"); err != nil {
			t.Fatalf("Once: %v", err)
		}
		if got := appr.callCount(); got != 1 {
			t.Errorf("approver asked %d times, want 1 — default grants should remember the first approval", got)
		}
		if !rec.ran.Load() {
			t.Error("approved tool never ran")
		}
	})
}

// TestNew_NoGate_ToolsUngated is the control for the wrap test: without WithGate the tool runs and
// its real output reaches the model — nothing is gated.
func TestNew_NoGate_ToolsUngated(t *testing.T) {
	t.Parallel()
	tool, rec := newRecordingTool(t, "plain", "OUTPUT")
	llm := &scriptedLLM{steps: []agentkit.Step{callStep("c1", "plain", "{}"), answerStep("ok")}}
	rt := runtime.New(llm, runtime.WithTools(toolset(t, tool)))

	if _, err := rt.Once(context.Background(), "go"); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if !rec.ran.Load() {
		t.Error("ungated tool did not run")
	}
	if got := llm.lastToolResult(t); got != "OUTPUT" {
		t.Errorf("tool result = %q, want %q (ungated, real output)", got, "OUTPUT")
	}
}

// TestNew_GrantsProvided_NotOverwritten pins that a caller-supplied Grants store is preserved: a
// seeded standing grant covers the Ask, so the tool runs even with a nil (unattended) Approver. If
// New had replaced the store with a fresh empty one, the Ask would fail closed and deny.
func TestNew_GrantsProvided_NotOverwritten(t *testing.T) {
	t.Parallel()
	tool, rec := newRecordingTool(t, "notify", "sent")
	policy := gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		if a.Kind == "notify" {
			return gate.AskWith(gate.RecallSession)
		}
		return gate.Allowed()
	})
	seeded := gate.NewMemGrants(gate.Grant{Kind: "notify", Target: ""})
	llm := &scriptedLLM{steps: []agentkit.Step{callStep("c1", "notify", "{}"), answerStep("ok")}}
	rt := runtime.New(llm,
		runtime.WithTools(toolset(t, tool)),
		runtime.WithGate(policy, seeded, nil), // nil Approver: only the seeded grant can allow it
	)

	if _, err := rt.Once(context.Background(), "go"); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if !rec.ran.Load() {
		t.Error("seeded grant did not cover the action — provided Grants was overwritten")
	}
	if got := llm.lastToolResult(t); got != "sent" {
		t.Errorf("tool result = %q, want %q", got, "sent")
	}
}

// --- Once / Session: gate install -------------------------------------------

// TestRuntime_Once_InstallsGateOnCtx pins that Once installs the permission machinery onto the ctx
// so it reaches a tool's OWN gate.Check (a self-gating host axis), not just the name wrapper: a
// Deny on that axis surfaces as the tool result and the tool never does its work.
func TestRuntime_Once_InstallsGateOnCtx(t *testing.T) {
	t.Parallel()
	tool, rec := newSelfGatingTool(t, "fetch", "net", "FETCHED")
	policy := gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		if a.Kind == "net" {
			return gate.Denied()
		}
		return gate.Allowed() // the tool NAME "fetch" is allowed; only its "net" axis is denied
	})
	llm := &scriptedLLM{steps: []agentkit.Step{callStep("c1", "fetch", "{}"), answerStep("ok")}}
	rt := runtime.New(llm,
		runtime.WithTools(toolset(t, tool)),
		runtime.WithGate(policy, nil, nil),
	)

	if _, err := rt.Once(context.Background(), "go"); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if got := llm.lastToolResult(t); !strings.Contains(got, "gate: denied") {
		t.Errorf("tool result = %q, want the self-gate denial (machinery reached the tool's Check)", got)
	}
	if rec.work.Load() {
		t.Error("self-gating tool did its work despite a Deny — gate machinery not installed on ctx")
	}
}

// TestRuntime_Once_NoGate_NoInstall pins the inverse: with no gate configured, prepare leaves ctx
// untouched, a tool's own gate.Check finds no machinery (open), and it runs freely.
func TestRuntime_Once_NoGate_NoInstall(t *testing.T) {
	t.Parallel()
	tool, rec := newSelfGatingTool(t, "fetch", "net", "FETCHED")
	llm := &scriptedLLM{steps: []agentkit.Step{callStep("c1", "fetch", "{}"), answerStep("ok")}}
	rt := runtime.New(llm, runtime.WithTools(toolset(t, tool)))

	if _, err := rt.Once(context.Background(), "go"); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if !rec.work.Load() {
		t.Error("no gate configured — self-check should be open and the tool should run")
	}
	if got := llm.lastToolResult(t); got != "FETCHED" {
		t.Errorf("tool result = %q, want %q (no gating)", got, "FETCHED")
	}
}

// --- session option composition ---------------------------------------------

// TestRuntime_SessionOpts_Order_ExtraOverridesBase pins that per-call options passed to Once/Session
// are applied AFTER the runtime's WithSession defaults, so a later same-setter option wins. Observed
// through the system prompt the runtime assembles into the conversation sent to the model.
func TestRuntime_SessionOpts_Order_ExtraOverridesBase(t *testing.T) {
	t.Parallel()
	llm := &scriptedLLM{steps: []agentkit.Step{answerStep("ok")}}
	rt := runtime.New(llm, runtime.WithSession(agentkit.WithSystem("BASE")))

	if _, err := rt.Once(context.Background(), "hi", agentkit.WithSystem("EXTRA")); err != nil {
		t.Fatalf("Once: %v", err)
	}
	conv := llm.convAt(0)
	if len(conv) == 0 {
		t.Fatal("model received an empty conversation")
	}
	if conv[0].Role != agentkit.RoleSystem {
		t.Fatalf("conv[0].Role = %q, want %q", conv[0].Role, agentkit.RoleSystem)
	}
	if conv[0].Content != "EXTRA" {
		t.Errorf("system prompt = %q, want %q (extra option must override the base)", conv[0].Content, "EXTRA")
	}
}

// TestRuntime_Session_WiresToolsAndSkills pins that a live Session hands the model the runtime's
// tool specs PLUS the progressive-disclosure skill loader. NOTE: TESTPLAN calls the loader
// "load_skill", but the implemented tool name is "skill_load" (agentkit.loadSkillToolName); the
// test asserts the real name.
func TestRuntime_Session_WiresToolsAndSkills(t *testing.T) {
	t.Parallel()
	tool, _ := newRecordingTool(t, "weather", "sunny")
	skills, err := agentkit.NewSkillSet(agentkit.Skill{
		Name:        "haiku",
		Description: "write a haiku",
		Body:        "5-7-5",
	})
	if err != nil {
		t.Fatalf("NewSkillSet: %v", err)
	}
	llm := &scriptedLLM{steps: []agentkit.Step{answerStep("hi")}}
	rt := runtime.New(llm, runtime.WithTools(toolset(t, tool)), runtime.WithSkills(skills))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess := rt.Session(ctx)
	defer sess.Close()

	sub := sess.Subscribe()
	sess.Submit("hello")
	waitTurnEnd(t, sub)

	names := map[string]bool{}
	for _, s := range llm.specsAt(0) {
		names[s.Name] = true
	}
	if !names["weather"] {
		t.Errorf("model did not see the runtime tool %q; saw %v", "weather", keys(names))
	}
	if !names["skill_load"] {
		t.Errorf("model did not see the skill loader %q; saw %v", "skill_load", keys(names))
	}
}

func waitTurnEnd(t *testing.T, sub <-chan agentkit.Event) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				t.Fatal("event stream closed before TurnEnd")
			}
			if _, done := ev.(agentkit.TurnEnd); done {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for TurnEnd")
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
