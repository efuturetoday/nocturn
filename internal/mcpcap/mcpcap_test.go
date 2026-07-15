package mcpcap_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/mcpcap"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// fakeMCP is a minimal remote MCP server: initialize, tools/list (one "echo"
// tool), tools/call (echoes the arguments, or returns callText if set). It
// records request count and the Authorization header it saw. sse switches
// responses to text/event-stream.
type fakeMCP struct {
	sse      bool
	callText string // non-empty: fixed tools/call result text

	mu       sync.Mutex
	requests int
	auth     []string // Authorization header per request
}

func (f *fakeMCP) start() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()

		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil { // notification
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "serverInfo": map[string]any{"name": "fake"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name": "echo", "description": "echoes input",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}},
			}}}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			text := p.Name + ":" + string(p.Arguments)
			if f.callText != "" {
				text = f.callText
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
		if f.sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "id: 1\ndata: \n\ndata: %s\n\n", body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func (f *fakeMCP) hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func allowWrite() capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Capability: "http.write", TargetGlob: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent},
	}}
}

func askWrite() capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Capability: "http.write", TargetGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
}

// autoNotifier resolves a pending HITL request immediately with the desired
// outcome, capturing the intents it was shown.
type autoNotifier struct {
	want    hitl.Outcome
	resolve func(token string) error

	mu      sync.Mutex
	intents []string
}

func (n *autoNotifier) Notify(intent string, options []hitl.Option) error {
	n.mu.Lock()
	n.intents = append(n.intents, intent)
	n.mu.Unlock()
	for _, o := range options {
		if o.Outcome == n.want {
			return n.resolve(o.Token)
		}
	}
	return errors.New("autoNotifier: no matching option")
}

func askGuard(want hitl.Outcome) (*gateway.Guard, *autoNotifier) {
	n := &autoNotifier{want: want}
	e := hitl.NewEngine([]byte("test-key"), n)
	n.resolve = e.Resolve
	return &gateway.Guard{Policy: askWrite(), Approvals: e, TTL: time.Second}, n
}

// connect builds a Conn over srvURL and runs the handshake + tools/list.
func connect(t *testing.T, srvURL string, guard *gateway.Guard, inj *secret.Injector, sc *secret.Scanner) *mcpcap.Conn {
	t.Helper()
	conn, err := mcpcap.New(mcpcap.Server{Name: "test", URL: srvURL}, guard, inj, sc, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return conn
}

func TestConn_E2E_ConnectListCall(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[sse], func(t *testing.T) {
			f := &fakeMCP{sse: sse}
			srv := f.start()
			defer srv.Close()

			conn := connect(t, srv.URL, &gateway.Guard{Policy: allowWrite()}, nil, nil)
			if err := conn.Connect(context.Background()); err != nil {
				t.Fatalf("connect: %v", err)
			}
			tools, err := conn.Tools(context.Background())
			if err != nil || len(tools) != 1 {
				t.Fatalf("tools = %+v, err=%v", tools, err)
			}
			if tools[0].Name != "test.echo" {
				t.Errorf("tool name = %q, want test.echo (namespaced)", tools[0].Name)
			}
			if tools[0].Description != "echoes input" {
				t.Errorf("description = %q", tools[0].Description)
			}
			out, err := tools[0].Invoke(context.Background(), `{"x":"1"}`)
			if err != nil || !strings.Contains(out, `echo:{"x":"1"}`) {
				t.Fatalf("invoke = %q, err=%v", out, err)
			}
		})
	}
}

// A denied connection performs NO HTTP: the effect closure is unreachable on
// a broker deny — the handshake itself never leaves the process.
func TestConn_Denied_NoHTTP(t *testing.T) {
	f := &fakeMCP{}
	srv := f.start()
	defer srv.Close()

	conn := connect(t, srv.URL, &gateway.Guard{Policy: capability.Policy{}}, nil, nil) // deny-by-default
	if err := conn.Connect(context.Background()); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if f.hits() != 0 {
		t.Fatal("a denied MCP connection must not reach the network")
	}
}

// On Ask, a human deny blocks the call before any HTTP; an approve lets it
// proceed — and the HITL prompt reads at the semantic level ("MCP <server>:
// <tool>"), not the raw transport POST.
func TestConn_Ask_SemanticIntent(t *testing.T) {
	f := &fakeMCP{}
	srv := f.start()
	defer srv.Close()

	guard, notifier := askGuard(hitl.Approved)
	conn := connect(t, srv.URL, guard, nil, nil)
	if err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	tools, err := conn.Tools(context.Background())
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if _, err := tools[0].Invoke(context.Background(), `{"x":"1"}`); err != nil {
		t.Fatalf("invoke: %v", err)
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	want := []string{"MCP test: connect (" + srv.URL + ")", "MCP test: connect (" + srv.URL + ")", "MCP test: list tools", "MCP test: echo"}
	if len(notifier.intents) != len(want) {
		t.Fatalf("intents = %q, want %d prompts", notifier.intents, len(want))
	}
	for i, w := range want {
		if notifier.intents[i] != w {
			t.Errorf("intent[%d] = %q, want %q", i, notifier.intents[i], w)
		}
	}
}

func TestConn_Ask_DeniedNoHTTP(t *testing.T) {
	f := &fakeMCP{}
	srv := f.start()
	defer srv.Close()

	guard, _ := askGuard(hitl.Denied)
	conn := connect(t, srv.URL, guard, nil, nil)
	if err := conn.Connect(context.Background()); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if f.hits() != 0 {
		t.Fatal("a human-denied MCP call must not reach the network")
	}
}

// The connection's own bearer (owner "mcp:test") is injected host-side on
// every request; a FOREIGN owner's binding at the same host never rides along.
func TestConn_CredentialInjection_OwnerScoped(t *testing.T) {
	f := &fakeMCP{}
	srv := f.start()
	defer srv.Close()

	store := secret.NewStore()
	owner := mcpcap.Owner("test")
	own := mcpcap.SecretName(owner, mcpcap.CredentialName)
	store.Set(own, []byte("own-token-123"))
	store.Set("plugin:x/oauth", []byte("foreign-token-456"))

	inj := secret.NewInjector(store)
	conn := connect(t, srv.URL, &gateway.Guard{Policy: allowWrite()}, inj, nil)
	inj.AddBinding(owner, secret.Binding{Secret: own, Capability: "http.write", Host: conn.Host(), Header: "Authorization", Prefix: "Bearer "})
	inj.AddBinding("plugin:x", secret.Binding{Secret: "plugin:x/oauth", Capability: "http.write", Host: conn.Host(), Header: "X-Api-Key", Prefix: ""})

	if err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.auth {
		if a != "Bearer own-token-123" {
			t.Errorf("request %d Authorization = %q, want the mcp:test bearer", i, a)
		}
	}
}

// A stored secret in the model-supplied tool arguments is exfiltration: the
// egress scan blocks the call before any byte leaves the process.
func TestConn_LeakScan_BlocksEgress(t *testing.T) {
	f := &fakeMCP{}
	srv := f.start()
	defer srv.Close()

	store := secret.NewStore()
	store.Set("vault-key", []byte("super-secret-value-42"))
	sc := secret.NewScanner(store)

	conn := connect(t, srv.URL, &gateway.Guard{Policy: allowWrite()}, nil, sc)
	if err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	tools, err := conn.Tools(context.Background())
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	before := f.hits()
	_, err = tools[0].Invoke(context.Background(), `{"x":"super-secret-value-42"}`)
	if !errors.Is(err, secret.ErrLeaked) {
		t.Fatalf("err = %v, want ErrLeaked", err)
	}
	if f.hits() != before {
		t.Fatal("a leaking tools/call must not reach the network")
	}
}

// A stored secret echoed back by the server is redacted at the transport
// boundary before it ever reaches the model — in both response shapes.
func TestConn_IngressRedaction(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[sse], func(t *testing.T) {
			f := &fakeMCP{sse: sse, callText: "your key is super-secret-value-42, keep it safe"}
			srv := f.start()
			defer srv.Close()

			store := secret.NewStore()
			store.Set("vault-key", []byte("super-secret-value-42"))
			sc := secret.NewScanner(store)

			conn := connect(t, srv.URL, &gateway.Guard{Policy: allowWrite()}, nil, sc)
			if err := conn.Connect(context.Background()); err != nil {
				t.Fatalf("connect: %v", err)
			}
			tools, err := conn.Tools(context.Background())
			if err != nil {
				t.Fatalf("tools: %v", err)
			}
			out, err := tools[0].Invoke(context.Background(), `{}`)
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			if strings.Contains(out, "super-secret-value-42") {
				t.Fatalf("result still contains the secret: %q", out)
			}
			if !strings.Contains(out, "[REDACTED]") {
				t.Fatalf("result = %q, want the secret redacted", out)
			}
		})
	}
}

// A server advertising a hostile tool name or a non-object schema is rejected
// fail-closed — nothing half-trusted enters the registry.
func TestConn_RejectsBadToolShapes(t *testing.T) {
	cases := map[string]map[string]any{
		"bad name":   {"name": "e cho!", "inputSchema": map[string]any{"type": "object"}},
		"bad schema": {"name": "echo", "inputSchema": map[string]any{"type": "string"}},
		"no schema":  {"name": "echo"},
	}
	for label, decl := range cases {
		t.Run(label, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     *int64 `json:"id"`
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.ID == nil {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				result := map[string]any{"protocolVersion": "2025-11-25"}
				if req.Method == "tools/list" {
					result = map[string]any{"tools": []map[string]any{decl}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
			}))
			defer srv.Close()

			conn := connect(t, srv.URL, &gateway.Guard{Policy: allowWrite()}, nil, nil)
			if err := conn.Connect(context.Background()); err != nil {
				t.Fatalf("connect: %v", err)
			}
			if _, err := conn.Tools(context.Background()); err == nil {
				t.Fatalf("Tools accepted a hostile %s", label)
			}
		})
	}
}
