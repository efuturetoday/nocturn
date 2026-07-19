package chat_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// scriptedModel returns a fixed sequence of steps and records every conversation
// it was shown, so a test can assert what the model saw (e.g. that Reset cleared
// the history). Turns are serialized by the loop, so no lock is needed.
type scriptedModel struct {
	steps []brain.Step
	calls int
	convs [][]brain.Message
}

func (m *scriptedModel) Next(_ context.Context, conv []brain.Message, _ []tool.Spec) (brain.Step, error) {
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

// startChatOn spins a chat over guard + tools with the scripted model and returns it
// with a subscription — the fixture for turn-ceremony tests (permissions, skills).
func startChatOn(t *testing.T, model brain.Model, guard *gateway.Guard, tools *tool.Registry) (*chat.Chat, <-chan chat.Event) {
	t.Helper()
	c := chat.New(brain.New(model), guard, chat.Meta{ID: "t0"}, chat.Charter{Tools: tools})
	sub, unsub := c.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { unsub(); cancel() })
	c.Start(ctx)
	return c, sub
}

// turnOK submits input and waits for its TurnEnd, failing the test on a turn error.
func turnOK(t *testing.T, c *chat.Chat, sub <-chan chat.Event, input string) {
	t.Helper()
	c.Submit(chat.SourceUser, input, "")
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-sub:
			if te, ok := e.(chat.TurnEndEvent); ok {
				if te.Err != nil {
					t.Fatalf("turn %q: %v", input, te.Err)
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for TurnEnd of %q", input)
		}
	}
}

// A turn threads the chat's permission context (carrying its epoch) through ctx
// down to the tool, and Reset rotates it to a fresh, different epoch.
func TestChat_ThreadsContext_ResetRotatesEpoch(t *testing.T) {
	var seen []capability.EpochID
	tools := []tool.Tool{{
		Spec: tool.Spec{Name: "probe"},
		Invoke: func(ctx context.Context, _ string) (string, error) {
			if c := capability.GrantsFrom(ctx); c != nil {
				seen = append(seen, c.Epoch)
			} else {
				seen = append(seen, 0)
			}
			return "ok", nil
		},
	}}
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "probe"}}}, {Answer: "one"},
		{ToolCalls: []brain.ToolCall{{Tool: "probe"}}}, {Answer: "two"},
	}}

	c, sub := startChatOn(t, model, &gateway.Guard{}, tool.NewRegistry().AddMany(tools...))

	turnOK(t, c, sub, "first")
	c.Reset() // ordered on the command loop: processed before the next Submit
	turnOK(t, c, sub, "second")

	if len(seen) != 2 {
		t.Fatalf("tool saw %d epochs, want 2", len(seen))
	}
	if seen[0] == 0 {
		t.Fatal("the tool saw the zero epoch — the chat epoch was not threaded through ctx")
	}
	if seen[0] == seen[1] {
		t.Fatalf("epoch did not rotate on Reset: both turns saw %d", seen[0])
	}
	// Revocation of the old epoch is behavioural and covered by
	// TestChat_Reset_RevokesGrantsAndClearsHistory (a session grant no longer
	// applies after Reset) — the registry is the Guard's private detail.
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
func TestChat_Reset_RevokesGrantsAndClearsHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	notifier := &autoNotifier{want: hitl.ApprovedSession}
	engine := hitl.NewEngine([]byte("test-key"), notifier)
	notifier.resolve = engine.Resolve

	guard := &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: engine,
		TTL:       time.Second,
	}
	netCap := netcap.New(guard)

	tools := []tool.Tool{{
		Spec: tool.Spec{Name: "net.fetch"},
		Invoke: func(ctx context.Context, _ string) (string, error) {
			resp, err := netCap.Fetch(ctx, secret.Request{URL: srv.URL})
			if err != nil {
				return "", err
			}
			return string(resp.Body), nil
		},
	}}
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "net.fetch"}}}, {Answer: "one"},
		{ToolCalls: []brain.ToolCall{{Tool: "net.fetch"}}}, {Answer: "two"},
	}}

	c, sub := startChatOn(t, model, guard, tool.NewRegistry().AddMany(tools...))

	turnOK(t, c, sub, "fetch it once")
	if notifier.calls != 1 {
		t.Fatalf("asked %d times on the first turn, want 1", notifier.calls)
	}

	c.Reset()

	turnOK(t, c, sub, "fetch it again")
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

// The chat owns the skill-activation set: it is threaded into every turn (so
// skill.load deduplicates within a conversation) and Reset clears it (a fresh
// conversation has no skills loaded).
func TestChat_SkillActiveSet_ResetClears(t *testing.T) {
	var seen []bool
	tools := []tool.Tool{{
		Spec: tool.Spec{Name: "probe"},
		Invoke: func(ctx context.Context, _ string) (string, error) {
			act := skill.ActiveFrom(ctx)
			if act == nil {
				t.Error("the turn did not stamp a skill.Active set")
				return "ok", nil
			}
			seen = append(seen, act.Has("x"))
			act.Mark("x")
			return "ok", nil
		},
	}}
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "probe"}}}, {Answer: "1"},
		{ToolCalls: []brain.ToolCall{{Tool: "probe"}}}, {Answer: "2"},
		{ToolCalls: []brain.ToolCall{{Tool: "probe"}}}, {Answer: "3"},
	}}
	c, sub := startChatOn(t, model, &gateway.Guard{}, tool.NewRegistry().AddMany(tools...))

	turnOK(t, c, sub, "a") // marks x
	turnOK(t, c, sub, "b") // x still active within the chat
	c.Reset()
	turnOK(t, c, sub, "c") // fresh set — x gone

	want := []bool{false, true, false}
	if len(seen) != 3 || seen[0] != want[0] || seen[1] != want[1] || seen[2] != want[2] {
		t.Fatalf("Has(x) across turns = %v, want %v (dedup within a chat, cleared on reset)", seen, want)
	}
}
