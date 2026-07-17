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
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// scriptedModel returns a fixed sequence of steps and records every conversation
// it was shown, so tests can assert what the model saw (e.g. a tool result).
type scriptedModel struct {
	steps []brain.Step
	calls int
	convs [][]brain.Message
}

func (m *scriptedModel) Next(ctx context.Context, conv []brain.Message, _ []tool.Spec) (brain.Step, error) {
	m.convs = append(m.convs, append([]brain.Message(nil), conv...))
	s := m.steps[m.calls]
	m.calls++
	if len(s.ToolCalls) == 0 {
		activity.Emit(ctx, activity.Token{Text: s.Answer}) // simulate streaming the final answer
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

func mkTool(name string, invoke func(context.Context, string) (string, error)) tool.Tool {
	return tool.Tool{Spec: tool.Spec{Name: name}, Invoke: invoke}
}

func TestBrain_ToolCallResultFedBackThenFinal(t *testing.T) {
	var gotArgs string
	reg := tool.NewRegistry().AddMany([]tool.Tool{
		mkTool("net.fetch", func(_ context.Context, args string) (string, error) {
			gotArgs = args
			return "PONG", nil
		}),
	}...)

	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "net.fetch", Args: `{"url":"http://example.com"}`}}},
		{Answer: "the page said PONG"},
	}}

	b := brain.New(model)
	ans, err := b.NewConversation(reg).Send(context.Background(), "what does example.com say?")
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
	b := brain.New(model)
	ctx := activity.WithSink(context.Background(), func(e activity.Event) {
		if tok, ok := e.(activity.Token); ok {
			streamed += tok.Text
		}
	})
	ans, err := b.NewConversation(tool.NewRegistry()).Send(ctx, "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ans != "streamed answer" || streamed != "streamed answer" {
		t.Fatalf("answer=%q streamed=%q", ans, streamed)
	}
}

// A tool's output is bounded HERE (at the brain) before it reaches the model, so
// one big result can't blow the context — regardless of which tool produced it.
func TestBrain_LongToolOutputBoundedForModel(t *testing.T) {
	big := strings.Repeat("x", 10000)
	reg := tool.NewRegistry().AddMany([]tool.Tool{
		mkTool("big", func(context.Context, string) (string, error) { return big, nil }),
	}...)

	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{ID: "1", Tool: "big"}}},
		{Answer: "done"},
	}}

	b := brain.New(model)
	if _, err := b.NewConversation(reg).Send(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}
	var toolMsg string
	for _, m := range model.convs[1] {
		if m.Role == "tool" {
			toolMsg = m.Content
		}
	}
	if len(toolMsg) >= len(big) || !strings.Contains(toolMsg, "truncated") {
		t.Fatalf("tool output not bounded for the model (len=%d)", len(toolMsg))
	}
}

// Multiple tool calls in one turn run CONCURRENTLY: each blocks at a barrier
// until all have started, so a sequential executor would deadlock here. Results
// are still stitched into history in CALL ORDER, not completion order.
func TestBrain_ParallelToolCallsRunConcurrently(t *testing.T) {
	// Under synctest the barrier itself proves concurrency: if the executor were serial,
	// the second tool could never send on `started` while the first is parked on
	// `proceed`, so every goroutine would be durably blocked and synctest would panic
	// with a deadlock — no 2s watchdog needed (go.dev/blog/testing-time).
	synctest.Test(t, func(t *testing.T) {
		const n = 3
		started := make(chan struct{}, n)
		proceed := make(chan struct{})
		mk := func(name, out string) tool.Tool {
			return mkTool(name, func(context.Context, string) (string, error) {
				started <- struct{}{} // announce start
				<-proceed             // wait for the barrier to open
				return out, nil
			})
		}
		reg := tool.NewRegistry().AddMany([]tool.Tool{mk("a", "ra"), mk("b", "rb"), mk("c", "rc")}...)
		model := &scriptedModel{steps: []brain.Step{
			{ToolCalls: []brain.ToolCall{{ID: "1", Tool: "a"}, {ID: "2", Tool: "b"}, {ID: "3", Tool: "c"}}},
			{Answer: "done"},
		}}
		b := brain.New(model)

		ansCh := make(chan string, 1)
		go func() { ans, _ := b.NewConversation(reg).Send(context.Background(), "go"); ansCh <- ans }()

		// All three must start before any is allowed to finish — impossible if serial.
		for range n {
			<-started
		}
		close(proceed)

		if ans := <-ansCh; ans != "done" {
			t.Fatalf("answer = %q, want done", ans)
		}
		// The model's second turn saw the results in call order a,b,c.
		var got []string
		for _, m := range model.convs[1] {
			if m.Role == "tool" {
				got = append(got, m.Content)
			}
		}
		if len(got) != 3 || got[0] != "ra" || got[1] != "rb" || got[2] != "rc" {
			t.Fatalf("tool results in history = %v, want [ra rb rc] in call order", got)
		}
	})
}

// Every invocation gets a unique id, and a nested call (a tool whose Invoke
// re-enters the Registry, like code.run → nocturn.call) records the enclosing
// call as its Parent — the basis for the observer to render concurrency + nesting.
func TestRegistry_NestedCallsCarryIDAndParent(t *testing.T) {
	var reg *tool.Registry
	var events []activity.ToolEvent
	inner := mkTool("inner", func(context.Context, string) (string, error) { return "in", nil })
	outer := mkTool("outer", func(ctx context.Context, _ string) (string, error) {
		return reg.Invoke(ctx, "inner", "{}") // nested: same ctx carries the parent id + sink
	})
	reg = tool.NewRegistry().AddMany([]tool.Tool{outer, inner}...)
	ctx := activity.WithSink(context.Background(), func(e activity.Event) {
		if ev, ok := e.(activity.ToolEvent); ok {
			events = append(events, ev)
		}
	})

	if _, err := reg.Invoke(ctx, "outer", "{}"); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	// Sequential nesting → outer start, inner start, inner end, outer end.
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4: %+v", len(events), events)
	}
	oStart, iStart, iEnd, oEnd := events[0], events[1], events[2], events[3]
	if oStart.Tool != "outer" || oStart.Parent != 0 {
		t.Fatalf("outer start = %+v, want root (Parent 0)", oStart)
	}
	if iStart.Tool != "inner" || iStart.Parent != oStart.ID {
		t.Fatalf("inner.Parent = %d, want outer.ID = %d", iStart.Parent, oStart.ID)
	}
	if oStart.ID == iStart.ID || oStart.ID == 0 || iStart.ID == 0 {
		t.Fatalf("ids must be unique and non-zero: outer=%d inner=%d", oStart.ID, iStart.ID)
	}
	if iEnd.ID != iStart.ID || oEnd.ID != oStart.ID {
		t.Fatalf("ToolEnd ids must match their ToolStart: %+v %+v", iEnd, oEnd)
	}
}

// A slow tool is cut off by ToolTimeout, and the timeout error is fed back so
// the model can move on instead of hanging. Under synctest the per-tool deadline fires
// on the fake clock, so the cutoff is instant and deterministic (go.dev/blog/testing-time).
func TestBrain_ToolTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := tool.NewRegistry().AddMany([]tool.Tool{
			mkTool("slow", func(ctx context.Context, _ string) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			}),
		}...)

		model := &scriptedModel{steps: []brain.Step{
			{ToolCalls: []brain.ToolCall{{Tool: "slow"}}},
			{Answer: "moved on"},
		}}

		b := brain.New(model, brain.WithToolTimeout(time.Second))
		ans, err := b.NewConversation(reg).Send(context.Background(), "call the slow tool")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if ans != "moved on" {
			t.Fatalf("answer = %q", ans)
		}
		if !convContains(model.convs[1], "deadline exceeded") {
			t.Fatal("the timeout error was not fed back to the model")
		}
	})
}

func TestBrain_MaxStepsExceeded(t *testing.T) {
	reg := tool.NewRegistry().AddMany([]tool.Tool{
		mkTool("noop", func(context.Context, string) (string, error) { return "", nil }),
	}...)

	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "noop"}}},
		{ToolCalls: []brain.ToolCall{{Tool: "noop"}}},
		{ToolCalls: []brain.ToolCall{{Tool: "noop"}}},
	}}

	b := brain.New(model, brain.WithMaxSteps(3))
	if _, err := b.NewConversation(reg).Send(context.Background(), "loop forever"); !errors.Is(err, brain.ErrMaxSteps) {
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

	b := brain.New(model)
	ans, err := b.NewConversation(tool.NewRegistry()).Send(context.Background(), "call a missing tool")
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
	reg := tool.NewRegistry().AddMany([]tool.Tool{
		mkTool("net.fetch", func(_ context.Context, args string) (string, error) {
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
	}...)

	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "net.fetch", Args: `{"wrong":"field"}`}}}, // missing url
		{Answer: "handled"},
	}}

	b := brain.New(model)
	if _, err := b.NewConversation(reg).Send(context.Background(), "fetch"); err != nil {
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

	netCap := netcap.New(&gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
		}},
	})

	reg := tool.NewRegistry().AddMany([]tool.Tool{
		mkTool("net.fetch", func(ctx context.Context, args string) (string, error) {
			var a struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil || a.URL == "" {
				return "", fmt.Errorf("invalid arguments")
			}
			resp, err := netCap.Fetch(ctx, secret.Request{URL: a.URL})
			if err != nil {
				return "", err
			}
			return string(resp.Body), nil
		}),
	}...)

	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "net.fetch", Args: fmt.Sprintf(`{"url":%q}`, srv.URL)}}},
		{Answer: "done"},
	}}

	b := brain.New(model)
	if _, err := b.NewConversation(reg).Send(context.Background(), "fetch the site"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !convContains(model.convs[1], "hello from the web") {
		t.Fatal("the gateway-fetched body was not fed back to the model")
	}
}

// A tool that sets Spec.MaxResult (a skill body) is fed back to the model
// UNtruncated; the same output from an ordinary tool is capped at maxToolOutput.
func TestBrain_MaxResultBypassesTruncation(t *testing.T) {
	big := strings.Repeat("x", 5000) // > maxToolOutput (4000)
	run := func(maxResult int) []brain.Message {
		reg := tool.NewRegistry().AddMany([]tool.Tool{{
			Spec:   tool.Spec{Name: "probe", MaxResult: maxResult},
			Invoke: func(context.Context, string) (string, error) { return big, nil },
		}}...)

		model := &scriptedModel{steps: []brain.Step{
			{ToolCalls: []brain.ToolCall{{ID: "1", Tool: "probe", Args: "{}"}}},
			{Answer: "done"},
		}}
		b := brain.New(model)
		if _, err := b.NewConversation(reg).Send(context.Background(), "go"); err != nil {
			t.Fatalf("run: %v", err)
		}
		return model.convs[len(model.convs)-1] // what the model saw on its final turn
	}

	if !convContains(run(64<<10), big) {
		t.Error("skill-sized result with MaxResult was truncated")
	}
	if convContains(run(0), big) {
		t.Error("ordinary result without MaxResult should be truncated at maxToolOutput")
	}
}
