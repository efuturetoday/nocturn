package agent_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// scriptedModel returns a fixed sequence of steps and records every conversation
// it was shown, so a test can assert what the model saw (e.g. that Reset cleared
// the history).
type scriptedModel struct {
	steps []brain.Step
	calls int
	convs [][]brain.Message
}

func (m *scriptedModel) Next(_ context.Context, conv []brain.Message, _ []brain.ToolSpec, _ func(string)) (brain.Step, error) {
	m.convs = append(m.convs, append([]brain.Message(nil), conv...))
	s := m.steps[m.calls]
	m.calls++
	return s, nil
}

func convContains(conv []brain.Message, substr string) bool {
	for _, m := range conv {
		if strings.Contains(m.Content, substr) {
			return true
		}
	}
	return false
}

// Ask threads the session's epoch through the context down to the tool, and
// Reset rotates it to a fresh, different epoch.
func TestSession_ThreadsEpoch_ResetRotatesIt(t *testing.T) {
	var seen []capability.EpochID
	tools := map[string]brain.Tool{
		"probe": {
			ToolSpec: brain.ToolSpec{Name: "probe"},
			Invoke: func(ctx context.Context, _ string) (string, error) {
				seen = append(seen, capability.EpochFrom(ctx))
				return "ok", nil
			},
		},
	}
	model := &scriptedModel{steps: []brain.Step{
		{ToolCall: &brain.ToolCall{Tool: "probe"}}, {Answer: "one"},
		{ToolCall: &brain.ToolCall{Tool: "probe"}}, {Answer: "two"},
	}}

	b := &brain.Brain{Model: model, Tools: tools}
	epochs := capability.NewEpochRegistry()
	s := agent.New(b, &gateway.Guard{Epochs: epochs}, epochs)

	if _, err := s.Ask(context.Background(), "first"); err != nil {
		t.Fatalf("first ask: %v", err)
	}
	s.Reset()
	if _, err := s.Ask(context.Background(), "second"); err != nil {
		t.Fatalf("second ask: %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("tool saw %d epochs, want 2", len(seen))
	}
	if seen[0] == 0 {
		t.Fatal("the tool saw the zero epoch — the session epoch was not threaded through ctx")
	}
	if seen[0] == seen[1] {
		t.Fatalf("epoch did not rotate on Reset: both turns saw %d", seen[0])
	}
	if !epochs.IsAlive(seen[1]) || epochs.IsAlive(seen[0]) {
		t.Fatal("after Reset the old epoch must be closed and the new one alive")
	}
}

// autoNotifier resolves the pending request immediately, picking the option that
// matches its desired outcome, and counts how often the human was asked.
type autoNotifier struct {
	want    hitl.Outcome
	resolve func(token string) error
	calls   int
}

func (n *autoNotifier) Notify(_ string, options []hitl.Option) error {
	n.calls++
	for _, o := range options {
		if o.Outcome == n.want {
			return n.resolve(o.Token)
		}
	}
	return errors.New("autoNotifier: no matching option")
}

// Reset revokes the "Allow this session" grants (so the same host is asked
// again) and starts a fresh conversation (so the model no longer sees the old
// history).
func TestSession_Reset_RevokesGrantsAndClearsHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	notifier := &autoNotifier{want: hitl.ApprovedSession}
	engine := hitl.NewEngine([]byte("test-key"), notifier)
	notifier.resolve = engine.Resolve

	epochs := capability.NewEpochRegistry()
	guard := &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Capability: "net.fetch", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: engine,
		Epochs:    epochs,
		TTL:       time.Second,
	}
	netCap := &gateway.Net{Guard: guard}

	tools := map[string]brain.Tool{
		"net.fetch": {
			ToolSpec: brain.ToolSpec{Name: "net.fetch"},
			Invoke: func(ctx context.Context, _ string) (string, error) {
				body, err := netCap.Fetch(ctx, secret.Request{URL: srv.URL}, nil)
				return string(body), err
			},
		},
	}
	model := &scriptedModel{steps: []brain.Step{
		{ToolCall: &brain.ToolCall{Tool: "net.fetch"}}, {Answer: "one"},
		{ToolCall: &brain.ToolCall{Tool: "net.fetch"}}, {Answer: "two"},
	}}

	b := &brain.Brain{Model: model, Tools: tools}
	s := agent.New(b, guard, epochs)

	if _, err := s.Ask(context.Background(), "fetch it once"); err != nil {
		t.Fatalf("first ask: %v", err)
	}
	if notifier.calls != 1 {
		t.Fatalf("asked %d times on the first turn, want 1", notifier.calls)
	}

	s.Reset()

	if _, err := s.Ask(context.Background(), "fetch it again"); err != nil {
		t.Fatalf("second ask: %v", err)
	}
	if notifier.calls != 2 {
		t.Fatalf("asked %d times total, want 2 (Reset must revoke the session grant)", notifier.calls)
	}

	// Fresh conversation: the model's view on the second turn must not carry the
	// first turn's input.
	last := model.convs[len(model.convs)-1]
	if convContains(last, "fetch it once") {
		t.Fatal("Reset did not clear the conversation history")
	}
}
