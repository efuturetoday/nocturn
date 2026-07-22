package agentkit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestAgentTool_Spec(t *testing.T) {
	tool := agentkit.AgentTool(agentkit.Agent{Name: "researcher"}, &stepLLM{}, nil)
	spec := tool.Spec()
	if spec.Name != "researcher" {
		t.Fatalf("Name = %q, want researcher", spec.Name)
	}
	if !strings.Contains(spec.Description, "sub-agent") {
		t.Fatalf("Description = %q, want it to mention sub-agent", spec.Description)
	}
	if spec.Parameters == nil || len(spec.Parameters.Required) != 1 || spec.Parameters.Required[0] != "input" {
		t.Fatalf("Parameters = %+v, want required 'input'", spec.Parameters)
	}
}

func TestAgentTool_RunsToFinalAnswer_AsToolResult(t *testing.T) {
	sub := &stepLLM{steps: []agentkit.Step{answerStep("sub result")}}
	tool := agentkit.AgentTool(agentkit.Agent{Name: "worker", Instructions: "do it"}, sub, nil)
	out, err := tool.Call(context.Background(), `{"input":"task"}`)
	if err != nil {
		t.Fatalf("Call err = %v", err)
	}
	if out != "sub result" {
		t.Fatalf("tool result = %q, want the sub-agent's final answer", out)
	}
}

func TestAgentTool_SharesParentSink_EventsHaveNonZeroFrame(t *testing.T) {
	sub := &stepLLM{steps: []agentkit.Step{answerStep("sub done")}}
	parent := &stepLLM{steps: []agentkit.Step{callStep("a", "helper", `{"input":"x"}`), answerStep("parent done")}}
	tools := newSet(t, agentkit.AgentTool(agentkit.Agent{Name: "helper"}, sub, nil))

	cs := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), cs.fn())
	if _, err := agentkit.Once(ctx, parent, "go", agentkit.WithTools(tools)); err != nil {
		t.Fatalf("Once err = %v", err)
	}

	// The AgentTool call gets id 1; every sub-agent event must carry that id as its Frame.
	events := cs.all()
	starts := toolStartEvents(events)
	if len(starts) == 0 {
		t.Fatal("no ToolStart for the sub-agent call")
	}
	agentCallID := starts[0].ID
	if agentCallID == 0 {
		t.Fatal("agent call id = 0, want non-zero")
	}

	var sawSubTurn bool
	for _, e := range events {
		if ts, ok := e.(agentkit.TurnStart); ok && ts.Frame != 0 {
			sawSubTurn = true
			if ts.Frame != agentCallID {
				t.Fatalf("sub-agent TurnStart.Frame = %d, want the agent call id %d", ts.Frame, agentCallID)
			}
		}
	}
	if !sawSubTurn {
		t.Fatal("no sub-agent TurnStart with a non-zero frame")
	}
}

func TestAgentTool_SubAgentInternalsNotPersisted(t *testing.T) {
	sub := &stepLLM{steps: []agentkit.Step{callStep("s1", "leaf", "{}"), answerStep("sub answer")}}
	leaf := newSet(t, echoTool(t, "leaf", "leaf out"))
	parent := &stepLLM{steps: []agentkit.Step{callStep("a", "helper", `{"input":"x"}`), answerStep("parent done")}}
	tools := newSet(t, agentkit.AgentTool(agentkit.Agent{Name: "helper"}, sub, leaf))

	store := &fakeStore{}
	if _, err := agentkit.Once(context.Background(), parent, "go",
		agentkit.WithTools(tools), agentkit.WithStore(store, "s")); err != nil {
		t.Fatalf("Once err = %v", err)
	}

	// Parent holds only the tool result (the sub's final answer), not the sub-agent's internals.
	h := store.history()
	if tr := toolResult(t, h); tr.Content != "sub answer" {
		t.Fatalf("parent tool result = %q, want the sub-agent's final answer", tr.Content)
	}
	for _, m := range h {
		if m.Content == "leaf out" {
			t.Fatalf("sub-agent internal message leaked into parent history: %+v", h)
		}
	}
}

func TestAgentTool_InheritsBudgetAndCounter(t *testing.T) {
	// Sub-agent draws the SAME token pool (its spend counts toward the parent limit) and CONTINUES
	// the id sequence (its own tool calls get later ids, not a reset).
	sub := &stepLLM{steps: []agentkit.Step{callStepT("s1", "leaf", "{}", tc(0, 0, 20)), answerStepT("sub", tc(0, 0, 20))}}
	leaf := newSet(t, echoTool(t, "leaf", "leaf out"))
	parent := &stepLLM{steps: []agentkit.Step{callStepT("a", "helper", `{"input":"x"}`, tc(0, 0, 60)), answerStep("parent done")}}
	tools := newSet(t, agentkit.AgentTool(agentkit.Agent{Name: "helper"}, sub, leaf))

	cs := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), cs.fn())
	_, err := agentkit.Once(ctx, parent, "go", agentkit.WithTools(tools), agentkit.WithTokenLimit(100))
	// Shared pool: sub's 40 + parent's 60 = 100 ≥ limit → parent turn stops with ErrTokenLimit.
	if !errors.Is(err, agentkit.ErrTokenLimit) {
		t.Fatalf("err = %v, want ErrTokenLimit from the shared budget", err)
	}

	// Continued counter: agent call id 1, the sub-agent's leaf call id 2 (no reset to 1).
	var ids []uint64
	for _, ts := range toolStartEvents(cs.all()) {
		ids = append(ids, ts.ID)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("ToolStart ids = %v, want [1 2] (counter continued into the sub-agent)", ids)
	}
}

func TestAgentTool_EnterSpawn_MaxDepth_AsToolResult(t *testing.T) {
	err := runNestedSpawn(t, agentkit.WithMaxDepth(1))
	if !errors.Is(err, agentkit.ErrMaxDepth) {
		t.Fatalf("nested ToolEnd err = %v, want ErrMaxDepth surfaced as a tool result", err)
	}
}

func TestAgentTool_EnterSpawn_MaxSpawns_AsToolResult(t *testing.T) {
	err := runNestedSpawn(t, agentkit.WithMaxSpawns(1))
	if !errors.Is(err, agentkit.ErrMaxSpawns) {
		t.Fatalf("nested ToolEnd err = %v, want ErrMaxSpawns surfaced as a tool result", err)
	}
}

func TestAgentTool_InvalidArgs_Error(t *testing.T) {
	tool := agentkit.AgentTool(agentkit.Agent{Name: "worker"}, &stepLLM{}, nil)
	_, err := tool.Call(context.Background(), "{not json")
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("err = %v, want an invalid-arguments error", err)
	}
}

func TestAgentTool_LeafHasNoAgentTools(t *testing.T) {
	sub := &stepLLM{steps: []agentkit.Step{answerStep("done")}}
	tool := agentkit.AgentTool(agentkit.Agent{Name: "leaf"}, sub, nil)
	if _, err := tool.Call(context.Background(), `{"input":"x"}`); err != nil {
		t.Fatalf("Call err = %v", err)
	}
	// A leaf sub-agent is handed no tools, so the model it drives sees an empty toolset.
	if specs := sub.specsAt(0); len(specs) != 0 {
		t.Fatalf("leaf sub-agent saw %d tools, want 0", len(specs))
	}
}

func TestAgentTool_OptsAppendedAfterDefaults(t *testing.T) {
	sub := &stepLLM{steps: []agentkit.Step{answerStep("done")}}
	// Caller opts are appended after the AgentTool defaults, so a later WithSystem wins.
	tool := agentkit.AgentTool(agentkit.Agent{Name: "worker", Instructions: "default sys"}, sub, nil,
		agentkit.WithSystem("override sys"))
	if _, err := tool.Call(context.Background(), `{"input":"x"}`); err != nil {
		t.Fatalf("Call err = %v", err)
	}
	conv := sub.convAt(0)
	if len(conv) == 0 || conv[0].Role != agentkit.RoleSystem || conv[0].Content != "override sys" {
		t.Fatalf("system prompt = %+v, want caller override to win", conv)
	}
}

// runNestedSpawn drives parent → helper (depth 1) → grandchild (depth 2 / spawn 2), returning the
// ToolEnd.Err seen for the refused grandchild spawn. The refusal is surfaced as a tool result, so the
// parent turn itself completes without crashing.
func runNestedSpawn(t *testing.T, spawnGuard agentkit.Option) error {
	t.Helper()
	grandchild := &stepLLM{steps: []agentkit.Step{answerStep("deep")}}
	// helper's LLM calls its own sub-agent (grandchild), which enterSpawn refuses.
	helperLLM := &stepLLM{steps: []agentkit.Step{callStep("g", "grandchild", `{"input":"y"}`), answerStep("helper done")}}
	helperTools := newSet(t, agentkit.AgentTool(agentkit.Agent{Name: "grandchild"}, grandchild, nil))
	parent := &stepLLM{steps: []agentkit.Step{callStep("a", "helper", `{"input":"x"}`), answerStep("parent done")}}
	parentTools := newSet(t, agentkit.AgentTool(agentkit.Agent{Name: "helper"}, helperLLM, helperTools))

	cs := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), cs.fn())
	_, err := agentkit.Once(ctx, parent, "go", agentkit.WithTools(parentTools), spawnGuard)
	if err != nil {
		t.Fatalf("parent turn err = %v, want nil (cap surfaced as a tool result, not a crash)", err)
	}

	for _, te := range toolEndEvents(cs.all()) {
		if te.Tool == "grandchild" {
			return te.Err
		}
	}
	t.Fatal("no ToolEnd for the refused grandchild spawn")
	return nil
}
