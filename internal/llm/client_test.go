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

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/llm"
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

	c := llm.New(srv.URL, "k", "auto")
	var streamed string
	step, err := c.Next(context.Background(), []brain.Message{{Role: "user", Content: "q"}}, nil,
		func(tok string) { streamed += tok })
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if step.ToolCall != nil || step.Answer != "The answer is 42." {
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

	c := llm.New(srv.URL, "k", "auto")
	step, err := c.Next(context.Background(), []brain.Message{{Role: "user", Content: "fetch"}}, nil, nil)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if step.ToolCall == nil || step.ToolCall.Tool != "net.fetch" {
		t.Fatalf("step = %+v, want tool call net.fetch", step)
	}
	if step.ToolCall.Args != `{"url":"https://x"}` {
		t.Fatalf("args = %q, want accumulated JSON", step.ToolCall.Args)
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

	c := llm.New(srv.URL, "test-key", "test-model")
	tools := []brain.ToolSpec{{
		Name:        "net.fetch",
		Description: "Fetch a URL",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`),
	}}
	if _, err := c.Next(context.Background(), []brain.Message{{Role: "user", Content: "hello"}}, tools, nil); err != nil {
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
