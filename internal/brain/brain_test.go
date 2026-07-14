package brain_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// scriptedModel returns a fixed sequence of steps and records every conversation
// it was shown, so tests can assert what the model saw (e.g. a tool result).
type scriptedModel struct {
	steps []brain.Step
	calls int
	convs [][]brain.Message
}

func (m *scriptedModel) Next(_ context.Context, conv []brain.Message, _ []brain.ToolSpec, onToken func(string)) (brain.Step, error) {
	m.convs = append(m.convs, append([]brain.Message(nil), conv...))
	s := m.steps[m.calls]
	m.calls++
	if onToken != nil && len(s.ToolCalls) == 0 {
		onToken(s.Answer) // simulate streaming the final answer
	}
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

func tool(name string, invoke func(context.Context, string) (string, error)) brain.Tool {
	return brain.Tool{ToolSpec: brain.ToolSpec{Name: name}, Invoke: invoke}
}

func TestBrain_ToolCallResultFedBackThenFinal(t *testing.T) {
	var gotArgs string
	reg := brain.NewRegistry([]brain.Tool{
		tool("net.fetch", func(_ context.Context, args string) (string, error) {
			gotArgs = args
			return "PONG", nil
		}),
	})
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "net.fetch", Args: `{"url":"http://example.com"}`}}},
		{Answer: "the page said PONG"},
	}}

	b := &brain.Brain{Model: model, Registry: reg}
	ans, err := b.Run(context.Background(), "what does example.com say?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans != "the page said PONG" {
		t.Fatalf("answer = %q", ans)
	}
	if gotArgs != `{"url":"http://example.com"}` {
		t.Fatalf("tool got args %q", gotArgs)
	}
	if len(model.convs) != 2 || !convContains(model.convs[1], "PONG") {
		t.Fatal("the tool result was not fed back to the model")
	}
}

func TestBrain_StreamsAnswerTokens(t *testing.T) {
	model := &scriptedModel{steps: []brain.Step{{Answer: "streamed answer"}}}
	var streamed string
	b := &brain.Brain{
		Model:    model,
		Registry: brain.NewRegistry(nil),
		OnToken:  func(tok string) { streamed += tok },
	}
	ans, err := b.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans != "streamed answer" || streamed != "streamed answer" {
		t.Fatalf("answer=%q streamed=%q", ans, streamed)
	}
}

// Several tool calls in one model turn run sequentially — in order, each result
// fed back before the next model turn — never concurrently.
func TestBrain_MultipleToolCallsRunSequentially(t *testing.T) {
	var order []string
	reg := brain.NewRegistry([]brain.Tool{
		tool("a", func(context.Context, string) (string, error) { order = append(order, "a"); return "ra", nil }),
		tool("b", func(context.Context, string) (string, error) { order = append(order, "b"); return "rb", nil }),
	})
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "a"}, {Tool: "b"}}},
		{Answer: "done"},
	}}

	b := &brain.Brain{Model: model, Registry: reg}
	ans, err := b.Run(context.Background(), "do a then b")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans != "done" {
		t.Fatalf("answer = %q", ans)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("run order = %v, want [a b] in sequence", order)
	}
	// The model's next turn saw BOTH tool results.
	if !convContains(model.convs[1], "ra") || !convContains(model.convs[1], "rb") {
		t.Fatal("both tool results must be fed back before the next model turn")
	}
}

// A slow tool is cut off by ToolTimeout, and the timeout error is fed back so
// the model can move on instead of hanging.
func TestBrain_ToolTimeout(t *testing.T) {
	reg := brain.NewRegistry([]brain.Tool{
		tool("slow", func(ctx context.Context, _ string) (string, error) {
			<-ctx.Done() // blocks until the per-tool deadline fires
			return "", ctx.Err()
		}),
	})
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "slow"}}},
		{Answer: "moved on"},
	}}

	b := &brain.Brain{Model: model, Registry: reg, ToolTimeout: 20 * time.Millisecond}
	ans, err := b.Run(context.Background(), "call the slow tool")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans != "moved on" {
		t.Fatalf("answer = %q", ans)
	}
	if !convContains(model.convs[1], "deadline exceeded") {
		t.Fatal("the timeout error was not fed back to the model")
	}
}

func TestBrain_MaxStepsExceeded(t *testing.T) {
	reg := brain.NewRegistry([]brain.Tool{
		tool("noop", func(context.Context, string) (string, error) { return "", nil }),
	})
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "noop"}}},
		{ToolCalls: []brain.ToolCall{{Tool: "noop"}}},
		{ToolCalls: []brain.ToolCall{{Tool: "noop"}}},
	}}

	b := &brain.Brain{Model: model, Registry: reg, MaxSteps: 3}
	if _, err := b.Run(context.Background(), "loop forever"); !errors.Is(err, brain.ErrMaxSteps) {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
	if model.calls != 3 {
		t.Fatalf("model called %d times, want 3", model.calls)
	}
}

func TestBrain_UnknownToolIsReportedNotFatal(t *testing.T) {
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "ghost"}}},
		{Answer: "recovered"},
	}}

	b := &brain.Brain{Model: model, Registry: brain.NewRegistry(nil)}
	ans, err := b.Run(context.Background(), "call a missing tool")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans != "recovered" {
		t.Fatalf("answer = %q", ans)
	}
	if !convContains(model.convs[1], "unknown tool ghost") {
		t.Fatal("the unknown-tool error was not fed back")
	}
}

// A bad-arguments error from a tool is fed back so the model can correct itself.
func TestBrain_ToolValidationErrorFedBack(t *testing.T) {
	reg := brain.NewRegistry([]brain.Tool{
		tool("net.fetch", func(_ context.Context, args string) (string, error) {
			var a struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %v", err)
			}
			if a.URL == "" {
				return "", fmt.Errorf("missing required field: url")
			}
			return "ok", nil
		}),
	})
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "net.fetch", Args: `{"wrong":"field"}`}}}, // missing url
		{Answer: "handled"},
	}}

	b := &brain.Brain{Model: model, Registry: reg}
	if _, err := b.Run(context.Background(), "fetch"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !convContains(model.convs[1], "missing required field: url") {
		t.Fatal("the validation error was not fed back to the model")
	}
}

// The whole path: brain -> gateway-guarded net.fetch (args validated) -> real
// HTTP -> result fed back to the model.
func TestBrain_Integration_FetchThroughGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello from the web"))
	}))
	defer srv.Close()

	netCap := &gateway.Net{Guard: &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Capability: "http.read", HostGlob: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent},
		}},
	}}

	reg := brain.NewRegistry([]brain.Tool{
		tool("net.fetch", func(ctx context.Context, args string) (string, error) {
			var a struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil || a.URL == "" {
				return "", fmt.Errorf("invalid arguments")
			}
			body, err := netCap.Fetch(ctx, secret.Request{URL: a.URL})
			return string(body), err
		}),
	})
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "net.fetch", Args: fmt.Sprintf(`{"url":%q}`, srv.URL)}}},
		{Answer: "done"},
	}}

	b := &brain.Brain{Model: model, Registry: reg}
	if _, err := b.Run(context.Background(), "fetch the site"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !convContains(model.convs[1], "hello from the web") {
		t.Fatal("the gateway-fetched body was not fed back to the model")
	}
}
