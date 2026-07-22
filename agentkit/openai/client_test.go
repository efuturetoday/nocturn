package openai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/openai"
)

// Client satisfies the model port. Compile-time so a signature drift breaks the build, not a run.
var _ agentkit.LLM = (*openai.Client)(nil)

func TestClient_ImplementsLLM(t *testing.T) {
	// The var above is the real assertion; this test documents it in the run output.
	var _ agentkit.LLM = (*openai.Client)(nil)
}

// sentRequest is the subset of the go-openai ChatCompletionRequest wire JSON the adapter fills.
// The httptest handler decodes into it so a test can assert what the adapter put on the wire.
type sentRequest struct {
	Path            string            `json:"-"`
	Model           string            `json:"model"`
	Messages        []json.RawMessage `json:"messages"`
	Tools           []wireTool        `json:"tools"`
	ReasoningEffort string            `json:"reasoning_effort"`
	MaxTokens       int               `json:"max_tokens"`
	Stream          bool              `json:"stream"`
	StreamOptions   *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

// newStreamServer stands in for the OpenAI endpoint: it records the incoming request into captured
// (if non-nil) and replays chunks as an SSE stream terminated by [DONE]. Never touches the network
// beyond loopback. captured may be nil when a test only cares about the response.
func newStreamServer(t *testing.T, captured *sentRequest, chunks ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
				return
			}
			if err := json.Unmarshal(body, captured); err != nil {
				t.Errorf("decode request body: %v", err)
				return
			}
			captured.Path = r.URL.Path
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ch := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", ch)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// captureSink records events in emission order for order-sensitive assertions. Next emits on the
// caller's goroutine, so no synchronization is needed.
type captureSink struct {
	events []agentkit.Event
}

func (s *captureSink) fn() func(agentkit.Event) {
	return func(e agentkit.Event) { s.events = append(s.events, e) }
}

// tokenTexts returns the Text of every Token event, in order.
func (s *captureSink) tokenTexts() []string {
	var out []string
	for _, e := range s.events {
		if tok, ok := e.(agentkit.Token); ok {
			out = append(out, tok.Text)
		}
	}
	return out
}

// thinkingTexts returns the Text of every Thinking event, in order.
func (s *captureSink) thinkingTexts() []string {
	var out []string
	for _, e := range s.events {
		if th, ok := e.(agentkit.Thinking); ok {
			out = append(out, th.Text)
		}
	}
	return out
}

func TestNew_BaseURLV1Suffix(t *testing.T) {
	tests := []struct {
		name    string
		baseURL func(srv *httptest.Server) string
	}{
		{"no trailing slash", func(srv *httptest.Server) string { return srv.URL }},
		{"trailing slash trimmed", func(srv *httptest.Server) string { return srv.URL + "/" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got sentRequest
			srv := newStreamServer(t, &got, `{"choices":[{"index":0,"delta":{"content":"ok"}}]}`)
			c := openai.New(tt.baseURL(srv), "key", "gpt-x")

			if _, err := c.Next(context.Background(), []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}, nil); err != nil {
				t.Fatalf("Next: %v", err)
			}
			if got.Path != "/v1/chat/completions" {
				t.Errorf("request path = %q, want %q (baseURL must get a single /v1)", got.Path, "/v1/chat/completions")
			}
		})
	}
}

func TestNext_FinalAnswer_StreamsTokens(t *testing.T) {
	sink := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), sink.fn())
	srv := newStreamServer(t, nil,
		`{"choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
	)
	c := openai.New(srv.URL, "key", "gpt-x")

	step, err := c.Next(ctx, []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if step.Answer != "Hello" {
		t.Errorf("Answer = %q, want %q", step.Answer, "Hello")
	}
	if len(step.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %v, want none", step.ToolCalls)
	}
	want := agentkit.TokenCount{Prompt: 10, Completion: 5, Total: 15}
	if step.Tokens != want {
		t.Errorf("Tokens = %+v, want %+v", step.Tokens, want)
	}
	if got := sink.tokenTexts(); !equalStrings(got, []string{"Hel", "lo"}) {
		t.Errorf("token order = %v, want [Hel lo]", got)
	}
}

func TestNext_ToolCalls_NativeIDPlumbing(t *testing.T) {
	srv := newStreamServer(t, nil,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"NYC\"}"}}]}}]}`,
	)
	c := openai.New(srv.URL, "key", "gpt-x")

	step, err := c.Next(context.Background(), []agentkit.Message{{Role: agentkit.RoleUser, Content: "weather?"}}, nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if step.Answer != "" {
		t.Errorf("Answer = %q, want empty (tool-call step)", step.Answer)
	}
	if len(step.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(step.ToolCalls))
	}
	got := step.ToolCalls[0]
	if got.ID != "call_abc" {
		t.Errorf("ID = %q, want the id from the first fragment (call_abc)", got.ID)
	}
	if got.Tool != "get_weather" {
		t.Errorf("Tool = %q, want get_weather", got.Tool)
	}
	if got.Args != `{"city":"NYC"}` {
		t.Errorf("Args = %q, want reassembled {\"city\":\"NYC\"}", got.Args)
	}
}

func TestNext_ToolCalls_IDFallback(t *testing.T) {
	// No id and no index in the fragment: idx defaults to 0, id is synthesized as agentkit_call_0.
	srv := newStreamServer(t, nil,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"type":"function","function":{"name":"now","arguments":"{}"}}]}}]}`,
	)
	c := openai.New(srv.URL, "key", "gpt-x")

	step, err := c.Next(context.Background(), []agentkit.Message{{Role: agentkit.RoleUser, Content: "time?"}}, nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(step.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(step.ToolCalls))
	}
	if step.ToolCalls[0].ID != "agentkit_call_0" {
		t.Errorf("ID = %q, want synthesized agentkit_call_0", step.ToolCalls[0].ID)
	}
}

func TestNext_ParallelToolCalls_AccumulatePerIndex(t *testing.T) {
	// Index 1 appears BEFORE index 0 across fragments: the result must preserve first-seen order
	// (1 then 0), not fuse or numerically sort, and keep per-index args separate.
	srv := newStreamServer(t, nil,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_1","type":"function","function":{"name":"second","arguments":"{\"b\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_0","type":"function","function":{"name":"first","arguments":"{\"a\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}},{"index":0,"function":{"arguments":"1}"}}]}}]}`,
	)
	c := openai.New(srv.URL, "key", "gpt-x")

	step, err := c.Next(context.Background(), []agentkit.Message{{Role: agentkit.RoleUser, Content: "go"}}, nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(step.ToolCalls) != 2 {
		t.Fatalf("ToolCalls len = %d, want 2", len(step.ToolCalls))
	}
	first, second := step.ToolCalls[0], step.ToolCalls[1]
	if first.Tool != "second" || first.ID != "call_1" || first.Args != `{"b":2}` {
		t.Errorf("call[0] = %+v, want first-seen index 1 (second/call_1/{\"b\":2})", first)
	}
	if second.Tool != "first" || second.ID != "call_0" || second.Args != `{"a":1}` {
		t.Errorf("call[1] = %+v, want index 0 (first/call_0/{\"a\":1})", second)
	}
}

func TestNext_ReasoningDeltas_EmitThinking(t *testing.T) {
	sink := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), sink.fn())
	srv := newStreamServer(t, nil,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"let me think"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
	)
	c := openai.New(srv.URL, "key", "gpt-x")

	step, err := c.Next(ctx, []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if step.Answer != "answer" {
		t.Errorf("Answer = %q, want just the content (reasoning must NOT fold in)", step.Answer)
	}
	if got := sink.thinkingTexts(); !equalStrings(got, []string{"let me think"}) {
		t.Errorf("Thinking events = %v, want [let me think]", got)
	}
	if strings.Contains(step.Answer, "think") {
		t.Errorf("Answer %q leaked reasoning text", step.Answer)
	}
}

func TestNext_EffortDefaultSent(t *testing.T) {
	// The default WithEffort reaches the wire as reasoning_effort. The ctx-override branch
	// (agentkit.EffortFrom) cannot be exercised here: the only setter is agentkit's unexported
	// withEffort — see the report. The default path is what an out-of-package caller can observe.
	var got sentRequest
	srv := newStreamServer(t, &got, `{"choices":[{"index":0,"delta":{"content":"ok"}}]}`)
	c := openai.New(srv.URL, "key", "gpt-x", openai.WithEffort(agentkit.EffortHigh))

	if _, err := c.Next(context.Background(), []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want high", got.ReasoningEffort)
	}
}

func TestNext_MaxTokensSet(t *testing.T) {
	tests := []struct {
		name string
		max  int
		want int // want on the wire; 0 means the field is omitted
	}{
		{"unset stays absent", 0, 0},
		{"positive is sent", 256, 256},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got sentRequest
			srv := newStreamServer(t, &got, `{"choices":[{"index":0,"delta":{"content":"ok"}}]}`)
			c := openai.New(srv.URL, "key", "gpt-x", openai.WithMaxTokens(tt.max))

			if _, err := c.Next(context.Background(), []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}, nil); err != nil {
				t.Fatalf("Next: %v", err)
			}
			if got.MaxTokens != tt.want {
				t.Errorf("max_tokens = %d, want %d", got.MaxTokens, tt.want)
			}
		})
	}
}

func TestNext_IncludeUsageRequested(t *testing.T) {
	var got sentRequest
	srv := newStreamServer(t, &got, `{"choices":[{"index":0,"delta":{"content":"ok"}}]}`)
	c := openai.New(srv.URL, "key", "gpt-x")

	if _, err := c.Next(context.Background(), []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !got.Stream {
		t.Error("stream = false, want true")
	}
	if got.StreamOptions == nil || !got.StreamOptions.IncludeUsage {
		t.Errorf("stream_options.include_usage not requested: %+v", got.StreamOptions)
	}
}

func TestNext_EmptyChoicesChunk_Skipped(t *testing.T) {
	// A chunk with no choices (and no usage) must be skipped without disturbing the answer.
	srv := newStreamServer(t, nil,
		`{"choices":[{"index":0,"delta":{"content":"a"}}]}`,
		`{"choices":[]}`,
		`{"choices":[{"index":0,"delta":{"content":"b"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	)
	c := openai.New(srv.URL, "key", "gpt-x")

	step, err := c.Next(context.Background(), []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if step.Answer != "ab" {
		t.Errorf("Answer = %q, want ab (empty-choices chunk skipped)", step.Answer)
	}
}

func TestNext_StreamCreateError_Wrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"nope"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := openai.New(srv.URL, "key", "gpt-x")

	_, err := c.Next(context.Background(), []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("Next: want error on stream-create failure, got nil")
	}
	if !strings.HasPrefix(err.Error(), "openai: create stream:") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "openai: create stream:")
	}
}

func TestNext_RecvError_Wrapped(t *testing.T) {
	// A mid-stream error frame surfaces from stream.Recv and must be wrapped as a recv error.
	srv := newStreamServer(t, nil,
		`{"choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		`{"error":{"message":"boom","type":"server_error"}}`,
	)
	c := openai.New(srv.URL, "key", "gpt-x")

	_, err := c.Next(context.Background(), []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("Next: want error on recv failure, got nil")
	}
	if !strings.HasPrefix(err.Error(), "openai: stream recv:") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "openai: stream recv:")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
