package workspace_test

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestFireAgent_UnknownAgent_Error: firing a name that isn't declared is an error, not a silent run.
func TestFireAgent_UnknownAgent_Error(t *testing.T) {
	w := openWS(t, fakeLLM{})
	if _, err := w.FireAgent(t.Context(), "nope", "task"); err == nil {
		t.Fatal("FireAgent on an unknown agent must error")
	}
}

// TestFireAgent_RunsOwnSubscribeLoop_PersistsToAgentStore: FireAgent drives its own subscribe loop
// to a final answer and persists the transcript to the AGENT store (source agent), not the user one.
func TestFireAgent_RunsOwnSubscribeLoop_PersistsToAgentStore(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "helper", nil)
	w := openWSDir(t, answerLLM{text: "the answer"}, dir)

	answer, err := w.FireAgent(t.Context(), "helper", "do the thing")
	if err != nil {
		t.Fatalf("FireAgent: %v", err)
	}
	if answer != "the answer" {
		t.Errorf("answer = %q, want %q", answer, "the answer")
	}

	runs, err := w.AgentRuns()
	if err != nil {
		t.Fatalf("AgentRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("agent runs = %d, want 1 persisted transcript", len(runs))
	}
	// The run landed in the agent store, and nothing leaked into the user store.
	if users, _ := w.Chats().List(); len(users) != 0 {
		t.Errorf("user store = %d chats, want 0 (an agent run must not persist there)", len(users))
	}
}

// TestFireAgent_Unattended_NilApprover_FailsClosedOnAsk: an unattended firing has no approver, so a
// gated action that would ask the human is DENIED — the tool result carries the denial.
func TestFireAgent_Unattended_NilApprover_FailsClosedOnAsk(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "neta", []string{"dns_resolve"}) // dns_resolve gates on the net axis (Ask)
	w := openWSDir(t, toolCallerLLM{}, dir)

	answer, err := w.FireAgent(t.Context(), "neta", "call:dns_resolve")
	if err != nil {
		t.Fatalf("FireAgent: %v", err)
	}
	// The net axis policy asks; with no approver the gate fails closed (gate.ErrDenied), surfaced to
	// the model as the tool result — which the fake echoes back as the answer.
	if !strings.Contains(answer, "denied") {
		t.Errorf("answer = %q, want it to carry the gate denial (unattended must fail closed)", answer)
	}
}

// TestFireAgent_CageIsAgentFilteredToolset: the firing's toolset is the workspace set filtered to the
// agent's declared tools — a call outside the cage is unreachable ("unknown tool"), an in-cage call
// runs.
func TestFireAgent_CageIsAgentFilteredToolset(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "caged", []string{"time_now"}) // cage = {time_now}; time_now is ungated
	w := openWSDir(t, toolCallerLLM{}, dir)

	// Out of cage: dns_resolve is filtered out, so the toolset reports it unknown.
	out, err := w.FireAgent(t.Context(), "caged", "call:dns_resolve")
	if err != nil {
		t.Fatalf("FireAgent (out of cage): %v", err)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("out-of-cage answer = %q, want an 'unknown tool' result (cage must exclude it)", out)
	}

	// In cage: time_now is reachable and returns a real result, not an unknown-tool error.
	in, err := w.FireAgent(t.Context(), "caged", "call:time_now")
	if err != nil {
		t.Fatalf("FireAgent (in cage): %v", err)
	}
	if strings.Contains(in, "unknown") {
		t.Errorf("in-cage answer = %q, want the tool to run (it is inside the cage)", in)
	}
}

// TestFireAgent_CtxCancel_ClosesSessionAndReturnsPartialAnswer: cancelling ctx mid-turn makes
// FireAgent close the session, join the reader goroutine, and return the partial answer streamed so
// far plus the ctx error. Run with -race: it joins <-done before reading the answer builder.
func TestFireAgent_CtxCancel_ClosesSessionAndReturnsPartialAnswer(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "slow", nil)
	b := &blockingLLM{emitted: make(chan struct{})}
	w := openWSDir(t, b, dir)

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		a, e := w.FireAgent(ctx, "slow", "long task")
		done <- result{a, e}
	}()

	<-b.emitted // the partial token has been streamed; the LLM is now blocked
	cancel()    // trip FireAgent's ctx.Done() path

	got := <-done
	if got.answer != "partial" {
		t.Errorf("answer = %q, want the partial %q streamed before cancel", got.answer, "partial")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", got.err)
	}
}
