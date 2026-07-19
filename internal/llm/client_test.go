package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/llm"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// mockStream serves the given chunks as an OpenAI SSE stream.
func mockStream(chunks ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestNext_StreamsAnswer(t *testing.T) {
	srv := mockStream(
		`{"choices":[{"delta":{"content":"The "}}]}`,
		`{"choices":[{"delta":{"content":"answer "}}]}`,
		`{"choices":[{"delta":{"content":"is 42."}}]}`,
	)
	defer srv.Close()

	c := llm.New(srv.URL, "k", "auto", "")
	var streamed string
	ctx := activity.WithSink(context.Background(), func(e activity.Event) {
		if tok, ok := e.(activity.Token); ok {
			streamed += tok.Text
		}
	})
	step, err := c.Next(ctx, []brain.Message{{Role: "user", Content: "q"}}, nil)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(step.ToolCalls) != 0 || step.Answer != "The answer is 42." {
		t.Fatalf("step = %+v, want final answer", step)
	}
	if streamed != "The answer is 42." {
		t.Fatalf("streamed = %q, want the answer token by token", streamed)
	}
}

func TestNext_AccumulatesStreamedToolCall(t *testing.T) {
	srv := mockStream(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"net.fetch","arguments":"{\"url\":\"htt"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ps://x\"}"}}]}}]}`,
	)
	defer srv.Close()

	c := llm.New(srv.URL, "k", "auto", "")
	step, err := c.Next(context.Background(), []brain.Message{{Role: "user", Content: "fetch"}}, nil)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(step.ToolCalls) != 1 || step.ToolCalls[0].Tool != "net.fetch" {
		t.Fatalf("step = %+v, want one tool call net.fetch", step)
	}
	if step.ToolCalls[0].Args != `{"url":"https://x"}` {
		t.Fatalf("args = %q, want accumulated JSON", step.ToolCalls[0].Args)
	}
	if step.ToolCalls[0].ID != "c1" {
		t.Fatalf("id = %q, want the streamed tool_call id c1", step.ToolCalls[0].ID)
	}
}

// Reasoning ("extended thinking") streams in delta.reasoning_content → surfaced as Thinking, not
// folded into the answer.
func TestNext_StreamsReasoning(t *testing.T) {
	srv := mockStream(
		`{"choices":[{"delta":{"reasoning_content":"let me think"}}]}`,
		`{"choices":[{"delta":{"content":"42"}}]}`,
	)
	defer srv.Close()

	c := llm.New(srv.URL, "k", "auto", "")
	var thought, answer string
	ctx := activity.WithSink(context.Background(), func(e activity.Event) {
		switch ev := e.(type) {
		case activity.Thinking:
			thought += ev.Text
		case activity.Token:
			answer += ev.Text
		}
	})
	step, err := c.Next(ctx, []brain.Message{{Role: "user", Content: "q"}}, nil)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if thought != "let me think" {
		t.Fatalf("thinking = %q, want the reasoning chunk", thought)
	}
	if answer != "42" || step.Answer != "42" {
		t.Fatalf("answer = %q / step %q, want just 42 (reasoning not in the answer)", answer, step.Answer)
	}
}

// reasoning_effort: a per-turn ctx value wins; else the client's global default; else unset.
func TestNext_ReasoningEffort(t *testing.T) {
	msg := []brain.Message{{Role: "user", Content: "q"}}
	for _, tc := range []struct {
		name          string
		clientDefault brain.Effort
		ctxEffort     brain.Effort // "" = no per-turn effort
		want          string
	}{
		{"ctx wins over client default", brain.EffortMedium, brain.EffortHigh, "high"},
		{"client default when no ctx", brain.EffortMedium, "", "medium"},
		{"unset → omitted (endpoint default)", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var req struct {
					ReasoningEffort string `json:"reasoning_effort"`
				}
				_ = json.Unmarshal(body, &req)
				got = req.ReasoningEffort
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()

			ctx := context.Background()
			if tc.ctxEffort != "" {
				ctx = brain.WithEffort(ctx, tc.ctxEffort)
			}
			if _, err := llm.New(srv.URL, "k", "auto", tc.clientDefault).Next(ctx, msg, nil); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("reasoning_effort = %q, want %q", got, tc.want)
			}
		})
	}
}

// When the endpoint omits tool_call ids, the adapter synthesizes stable ones so
// the assistant call and its result can still be matched by id on the next turn.
func TestNext_SynthesizesMissingToolCallID(t *testing.T) {
	srv := mockStream(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"dns.resolve","arguments":"{}"}}]}}]}`,
	)
	defer srv.Close()

	c := llm.New(srv.URL, "k", "auto", "")
	step, err := c.Next(context.Background(), []brain.Message{{Role: "user", Content: "go"}}, nil)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(step.ToolCalls) != 1 || step.ToolCalls[0].ID != "nocturn_call_0" {
		t.Fatalf("id = %q, want synthesized nocturn_call_0", step.ToolCalls[0].ID)
	}
}

// Prior tool turns are replayed to the model as NATIVE tool_calls / role=tool
// messages matched by tool_call_id — not flattened to text.
func TestNext_BuildsNativeToolHistory(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	conv := []brain.Message{
		{Role: "user", Content: "resolve example.com"},
		{Role: "assistant", ToolCalls: []brain.ToolCall{{ID: "call_9", Tool: "dns.resolve", Args: `{"host":"example.com"}`}}},
		{Role: "tool", ToolCallID: "call_9", Content: "93.184.216.34"},
	}
	c := llm.New(srv.URL, "test-key", "auto", "")
	if _, err := c.Next(context.Background(), conv, nil); err != nil {
		t.Fatalf("next: %v", err)
	}

	var req struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}

	var asst, toolMsg bool
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "[tool result]") {
			t.Fatalf("history still uses the [tool result] text prefix: %q", m.Content)
		}
		if m.Role == "assistant" && len(m.ToolCalls) == 1 {
			tc := m.ToolCalls[0]
			if tc.ID != "call_9" || tc.Type != "function" || tc.Function.Name != "dns.resolve" || tc.Function.Arguments != `{"host":"example.com"}` {
				t.Fatalf("assistant tool_call = %+v, want native call_9 dns.resolve", tc)
			}
			asst = true
		}
		if m.Role == "tool" {
			if m.ToolCallID != "call_9" || m.Content != "93.184.216.34" {
				t.Fatalf("tool message = %+v, want role=tool tied to call_9", m)
			}
			toolMsg = true
		}
	}
	if !asst {
		t.Fatal("no native assistant tool_calls message in the request")
	}
	if !toolMsg {
		t.Fatal("no native role=tool result message in the request")
	}
}

// Several tool calls in one turn (different stream indices) must be kept SEPARATE,
// each with its own accumulated name and args — not concatenated into one call.
func TestNext_MultipleToolCallsKeptSeparate(t *testing.T) {
	srv := mockStream(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"http.read","arguments":"{\"url\":\"a\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"dns.resolve","arguments":"{\"host\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"b\"}"}}]}}]}`,
	)
	defer srv.Close()

	c := llm.New(srv.URL, "k", "auto", "")
	step, err := c.Next(context.Background(), []brain.Message{{Role: "user", Content: "do both"}}, nil)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(step.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2 (indices must not be merged): %+v", len(step.ToolCalls), step.ToolCalls)
	}
	if step.ToolCalls[0].Tool != "http.read" || step.ToolCalls[0].Args != `{"url":"a"}` {
		t.Fatalf("call[0] = %+v, want http.read {url:a}", step.ToolCalls[0])
	}
	if step.ToolCalls[1].Tool != "dns.resolve" || step.ToolCalls[1].Args != `{"host":"b"}` {
		t.Fatalf("call[1] = %+v, want dns.resolve {host:b}", step.ToolCalls[1])
	}
}

// The request carries the bearer, the model, and each tool with its JSON Schema.
func TestNext_SendsToolSchemasAndAuth(t *testing.T) {
	var gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := llm.New(srv.URL, "test-key", "test-model", "")
	tools := []tool.Spec{{
		Name:        "net.fetch",
		Description: "Fetch a URL",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`),
	}}
	if _, err := c.Next(context.Background(), []brain.Message{{Role: "user", Content: "hello"}}, tools); err != nil {
		t.Fatalf("next: %v", err)
	}

	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q", gotAuth)
	}

	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
		Tools  []struct {
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	if req.Model != "test-model" || !req.Stream {
		t.Fatalf("model=%q stream=%v", req.Model, req.Stream)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "net.fetch" ||
		!strings.Contains(string(req.Tools[0].Function.Parameters), `"required":["url"]`) {
		t.Fatalf("tool schema not sent: %+v", req.Tools)
	}
}
