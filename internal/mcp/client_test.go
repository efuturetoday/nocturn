package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/internal/mcp"
)

// httpTransport is the plain, ungated transport for protocol tests: POST the
// body with the client-supplied headers and hand back the response stream.
// (The gated production transport lives in internal/mcp/conn.go.)
func httpTransport(url string) mcp.Transport {
	return func(ctx context.Context, body []byte, header http.Header) (*mcp.Response, error) {
		rq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		maps.Copy(rq.Header, header)
		rp, err := http.DefaultClient.Do(rq)
		if err != nil {
			return nil, err
		}
		if rp.StatusCode < 200 || rp.StatusCode >= 300 {
			rp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", rp.StatusCode)
		}
		return &mcp.Response{ContentType: rp.Header.Get("Content-Type"), Header: rp.Header, Body: rp.Body}, nil
	}
}

// fakeServer is a minimal JSON-RPC MCP server for tests. results maps a method
// to its result; errFor maps a method to an rpc error message. Notifications
// are answered 202 and recorded. sse switches the response shape to a
// text/event-stream with a priming event and an interleaved notification
// before the response — exactly what a real streaming server does.
type fakeServer struct {
	t       *testing.T
	results map[string]any
	errFor  map[string]string
	sse     bool

	mu            sync.Mutex
	notifications []string
	headers       []http.Header // request headers, in arrival order
}

func (f *fakeServer) start() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()

		if req.ID == nil { // a notification: 202 Accepted, no body (spec)
			f.mu.Lock()
			f.notifications = append(f.notifications, req.Method)
			f.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			return
		}

		w.Header().Set("Mcp-Session-Id", "sess-1")
		resp := map[string]any{"jsonrpc": "2.0", "id": *req.ID}
		if msg, isErr := f.errFor[req.Method]; isErr {
			resp["error"] = map[string]any{"code": -32000, "message": msg}
		} else if req.Method == "tools/call" {
			// echo the arguments so the test can assert the round-trip.
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			resp["result"] = map[string]any{"content": []map[string]any{
				{"type": "text", "text": p.Name + ":" + string(p.Arguments)},
			}}
		} else {
			resp["result"] = f.results[req.Method]
		}
		body, _ := json.Marshal(resp)

		if f.sse {
			w.Header().Set("Content-Type", "text/event-stream")
			// Priming event (id + empty data), a comment, an interleaved server
			// notification, then the response — per the Streamable HTTP spec.
			fmt.Fprint(w, "id: 1\ndata: \n\n")
			fmt.Fprint(w, ": keep-alive\n")
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
			fmt.Fprintf(w, "id: 2\ndata: %s\n\n", body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func initResult() map[string]any {
	return map[string]any{"protocolVersion": "2025-11-25", "serverInfo": map[string]any{"name": "fake"}}
}

func toolsResult() map[string]any {
	return map[string]any{"tools": []map[string]any{
		{"name": "echo", "description": "echoes input", "inputSchema": map[string]any{"type": "object"}},
	}}
}

func TestClient_HandshakeListCall(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[sse], func(t *testing.T) {
			f := &fakeServer{t: t, sse: sse, results: map[string]any{
				"initialize": initResult(), "tools/list": toolsResult(),
			}}
			srv := f.start()
			defer srv.Close()

			c := mcp.New(httpTransport(srv.URL))
			if err := c.Initialize(context.Background()); err != nil {
				t.Fatalf("initialize: %v", err)
			}
			if c.Session() != "sess-1" {
				t.Errorf("session = %q, want sess-1", c.Session())
			}
			f.mu.Lock()
			notified := len(f.notifications) == 1 && f.notifications[0] == "notifications/initialized"
			f.mu.Unlock()
			if !notified {
				t.Errorf("notifications = %v, want [notifications/initialized]", f.notifications)
			}

			tools, err := c.ListTools(context.Background())
			if err != nil || len(tools) != 1 || tools[0].Name != "echo" || tools[0].Description != "echoes input" {
				t.Fatalf("tools = %+v, err=%v", tools, err)
			}

			out, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"x":1}`))
			if err != nil || !strings.Contains(out, `echo:{"x":1}`) {
				t.Fatalf("call = %q, err=%v", out, err)
			}
		})
	}
}

// After initialize, every request must carry the session id the server assigned
// and the negotiated MCP-Protocol-Version (both spec MUSTs); the initialize
// request itself carries neither, but must list both accepted content types.
func TestClient_EchoesSessionAndProtocolHeaders(t *testing.T) {
	f := &fakeServer{t: t, results: map[string]any{"initialize": initResult(), "tools/list": toolsResult()}}
	srv := f.start()
	defer srv.Close()

	c := mcp.New(httpTransport(srv.URL))
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatalf("list: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.headers) < 3 { // initialize, initialized-notification, tools/list
		t.Fatalf("got %d requests, want 3", len(f.headers))
	}
	first, last := f.headers[0], f.headers[len(f.headers)-1]
	if a := first.Get("Accept"); !strings.Contains(a, "application/json") || !strings.Contains(a, "text/event-stream") {
		t.Errorf("Accept = %q, want both content types listed", a)
	}
	if s := first.Get("Mcp-Session-Id"); s != "" {
		t.Errorf("initialize carried a session id %q, want none", s)
	}
	if s := last.Get("Mcp-Session-Id"); s != "sess-1" {
		t.Errorf("Mcp-Session-Id = %q, want sess-1", s)
	}
	if v := last.Get("Mcp-Protocol-Version"); v != "2025-11-25" {
		t.Errorf("MCP-Protocol-Version = %q, want 2025-11-25", v)
	}
}

// A server negotiating a protocol version we do not support fails Initialize
// (fail closed — the spec says the client SHOULD disconnect).
func TestClient_UnsupportedProtocolVersion(t *testing.T) {
	f := &fakeServer{t: t, results: map[string]any{
		"initialize": map[string]any{"protocolVersion": "1999-01-01"},
	}}
	srv := f.start()
	defer srv.Close()
	err := mcp.New(httpTransport(srv.URL)).Initialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsupported server protocol version") {
		t.Fatalf("err = %v, want unsupported-version failure", err)
	}
}

// tools/list follows nextCursor pagination across pages and joins the results.
func TestClient_ListTools_Paginated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64 `json:"id"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := map[string]any{
			"tools":      []map[string]any{{"name": "a", "inputSchema": map[string]any{"type": "object"}}},
			"nextCursor": "page2",
		}
		if req.Params.Cursor == "page2" {
			result = map[string]any{
				"tools": []map[string]any{{"name": "b", "inputSchema": map[string]any{"type": "object"}}},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer srv.Close()

	tools, err := mcp.New(httpTransport(srv.URL)).ListTools(context.Background())
	if err != nil || len(tools) != 2 || tools[0].Name != "a" || tools[1].Name != "b" {
		t.Fatalf("tools = %+v, err=%v — want [a b] across two pages", tools, err)
	}
}

// A JSON-RPC error from the server surfaces as a Go error.
func TestClient_ServerError(t *testing.T) {
	f := &fakeServer{t: t, errFor: map[string]string{"tools/list": "boom"}}
	srv := f.start()
	defer srv.Close()
	if _, err := mcp.New(httpTransport(srv.URL)).ListTools(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the server error surfaced", err)
	}
}

// A tool result with isError:true is returned as an error (the model sees a
// failed tool call it can correct), carrying the content text.
func TestClient_ToolIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID int64 `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": "bad args"}},
		}})
	}))
	defer srv.Close()
	if _, err := mcp.New(httpTransport(srv.URL)).CallTool(context.Background(), "x", nil); err == nil || !strings.Contains(err.Error(), "bad args") {
		t.Fatalf("err = %v, want the isError content surfaced", err)
	}
}

// A response whose id does not match the request is rejected (fail closed) —
// the client never accepts a stray or replayed response as its own.
func TestClient_MismatchedResponseID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 999, "result": map[string]any{}})
	}))
	defer srv.Close()
	if _, err := mcp.New(httpTransport(srv.URL)).ListTools(context.Background()); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v, want an id-mismatch failure", err)
	}
}

// An SSE stream that ends without ever carrying our response is an error, not
// a silent empty result.
func TestClient_SSEWithoutResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
	}))
	defer srv.Close()
	if _, err := mcp.New(httpTransport(srv.URL)).ListTools(context.Background()); err == nil || !strings.Contains(err.Error(), "without a response") {
		t.Fatalf("err = %v, want stream-ended failure", err)
	}
}
