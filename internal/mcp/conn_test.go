package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// mcpProbe is a minimal MCP server for the gated-transport tests: it answers
// initialize + tools/list (one tool) + tools/call, and records what tools/call
// received (count, Authorization header, args, and the wire tool name) so a test
// can assert what actually crossed the boundary.
type mcpProbe struct {
	toolName string // advertised (and expected on the wire); default "echo"
	callText string // text returned by tools/call; empty = echo name+args

	mu       sync.Mutex
	calls    int
	lastAuth string
	lastName string
	lastArgs string
}

func (p *mcpProbe) start() *httptest.Server {
	name := p.toolName
	if name == "" {
		name = "echo"
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": *req.ID}
		switch req.Method {
		case "initialize":
			resp["result"] = map[string]any{"protocolVersion": "2025-11-25"}
		case "tools/list":
			resp["result"] = map[string]any{"tools": []map[string]any{
				{"name": name, "description": "d", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			var pr struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &pr)
			p.mu.Lock()
			p.calls++
			p.lastAuth = r.Header.Get("Authorization")
			p.lastName = pr.Name
			p.lastArgs = string(pr.Arguments)
			p.mu.Unlock()
			text := p.callText
			if text == "" {
				text = pr.Name + ":" + string(pr.Arguments)
			}
			resp["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// dialTool connects to srv over a gated Conn and returns its single exposed tool.
// Discovery runs on an OPEN context (no gate machinery) — the setup handshake
// must not prompt, mirroring installMCP.
func dialTool(t *testing.T, srv mcp.Server, creds *secret.Injector, scanner *secret.Scanner) agentkit.Tool {
	t.Helper()
	conn, err := mcp.NewConn(srv, creds, scanner)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	if err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	mtools, err := conn.Tools(context.Background())
	if err != nil || len(mtools) != 1 {
		t.Fatalf("Tools = %v, err=%v", mtools, err)
	}
	return mtools[0]
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Host
}

func allowAll() context.Context {
	return gate.With(context.Background(), gate.PolicyFunc(func(gate.Action) gate.Ruling {
		return gate.Allowed()
	}), nil, nil)
}

// A tools/call gates on the net axis for the server host: a policy denying that
// host blocks the call, and the request never reaches the server. Proves the
// runtime gating and the setup(open)/runtime(gated) ctx split.
func TestMCP_CallGatesOnNetKind_DeniedHostBlocked(t *testing.T) {
	p := &mcpProbe{}
	srv := p.start()
	defer srv.Close()

	tool := dialTool(t, mcp.Server{Name: "srv", URL: srv.URL}, nil, nil)
	deniedHost := hostOf(t, srv.URL)
	deny := gate.With(context.Background(), gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		if a.Kind == tools.NetKind && a.Target == deniedHost {
			return gate.Denied()
		}
		return gate.Allowed()
	}), nil, nil)

	out, err := tool.Call(deny, `{}`)
	if !errors.Is(err, gate.ErrDenied) {
		t.Fatalf("Call err = %v (out=%q), want gate.ErrDenied", err, out)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != 0 {
		t.Fatalf("server saw %d tools/call, want 0 (denied before the wire)", p.calls)
	}
}

// A secret smuggled into the tool arguments is caught by the egress scan before
// the request leaves the box.
func TestMCP_EgressScanBlocksSmuggledSecret(t *testing.T) {
	const val = "SUPERSECRETVALUE123"
	store := secret.NewStore()
	store.Set("api", []byte(val))
	sc := secret.NewScanner(store)

	p := &mcpProbe{}
	srv := p.start()
	defer srv.Close()

	tool := dialTool(t, mcp.Server{Name: "srv", URL: srv.URL}, nil, sc)
	out, err := tool.Call(allowAll(), `{"x":"`+val+`"}`)
	if err == nil || !strings.Contains(err.Error(), "egress blocked") {
		t.Fatalf("Call err = %v (out=%q), want an egress-blocked error", err, out)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != 0 {
		t.Fatalf("server received the smuggled secret (%d calls)", p.calls)
	}
}

// A stored vault value echoed back in the response is redacted before it reaches
// the model.
func TestMCP_IngressRedaction(t *testing.T) {
	const val = "SUPERSECRETVALUE123"
	store := secret.NewStore()
	store.Set("api", []byte(val))
	sc := secret.NewScanner(store)

	p := &mcpProbe{callText: "token=" + val + " done"}
	srv := p.start()
	defer srv.Close()

	tool := dialTool(t, mcp.Server{Name: "srv", URL: srv.URL}, nil, sc)
	out, err := tool.Call(allowAll(), `{}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if strings.Contains(out, val) {
		t.Fatalf("response leaked a stored secret: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %q", out)
	}
}

// A token seeded in the vault under SecretName is injected host-side as the
// Authorization bearer — the model's args never carry it.
func TestMCP_CredentialInjectedHostSide(t *testing.T) {
	p := &mcpProbe{}
	srv := p.start()
	defer srv.Close()

	store := secret.NewStore()
	// The binding names the secret SecretName(name, conn.host) where host carries the port (u.Host),
	// so the stored key must too.
	store.Set(mcp.SecretName("srv", hostOf(t, srv.URL)), []byte("TOK123"))
	inj := secret.NewInjector(store)

	tool := dialTool(t, mcp.Server{Name: "srv", URL: srv.URL, Auth: "token"}, inj, nil)
	if _, err := tool.Call(allowAll(), `{}`); err != nil {
		t.Fatalf("Call: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastAuth != "Bearer TOK123" {
		t.Fatalf("Authorization = %q, want Bearer TOK123", p.lastAuth)
	}
	if strings.Contains(p.lastArgs, "TOK123") {
		t.Fatalf("the token leaked into the model-supplied args: %q", p.lastArgs)
	}
}

// A remote tool name with characters OpenAI forbids (a dot) is sanitized for the
// exposed agentkit name, but the server is still called with its ORIGINAL name.
func TestMCP_ToolNameSanitizedButCalledWithOriginal(t *testing.T) {
	p := &mcpProbe{toolName: "github.create_issue"}
	srv := p.start()
	defer srv.Close()

	tool := dialTool(t, mcp.Server{Name: "gh", URL: srv.URL}, nil, nil)
	name := tool.Spec().Name
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(name) {
		t.Fatalf("exposed tool name %q violates the OpenAI charset", name)
	}
	if strings.Contains(name, ".") {
		t.Fatalf("exposed name %q still carries a dot", name)
	}
	if _, err := tool.Call(allowAll(), `{}`); err != nil {
		t.Fatalf("Call: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastName != "github.create_issue" {
		t.Fatalf("server called with %q, want the original name github.create_issue", p.lastName)
	}
}
