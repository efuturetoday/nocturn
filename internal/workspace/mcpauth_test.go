package workspace_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/workspace"
)

// fakeMCPServer stands up a TLS server that plays BOTH the MCP resource and its authorization server:
// a 401 probe pointing at protected-resource metadata, authorization-server metadata advertising a
// registration endpoint, dynamic client registration, and a token endpoint. It returns the base URL
// and a client that trusts the test cert (to thread through discovery and the code exchange).
func fakeMCPServer(t *testing.T) (base string, client *http.Client) {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	// The MCP endpoint: an unauthenticated probe is challenged with the resource-metadata URL.
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"resource":              srv.URL + "/mcp",
			"authorization_servers": []string{srv.URL},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
			"registration_endpoint":  srv.URL + "/register",
			"scopes_supported":       []string{"repo", "read"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"client_id": "dyn-client-123"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte("access_token=AT&token_type=bearer&refresh_token=RT&expires_in=3600"))
	})
	srv = httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, srv.Client()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// seedDiscoverServer writes a discover-mode mcp.json for a server pointing at base/mcp.
func seedDiscoverServer(t *testing.T, wsDir, name, base string) {
	t.Helper()
	dir := filepath.Join(wsDir, "mcp", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"url":"` + base + `/mcp","auth":"oauth"}`
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Begin runs the full spec discovery + dynamic registration and returns a consent URL bound to the
// loopback redirect and carrying PKCE + the RFC 8707 resource; Complete then exchanges the code and
// persists the token and provider record into the server's folder shard. This is the shared core both
// the CLI (loopback) and the companion app (relayed code) drive.
func TestMCPAuth_BeginComplete_PersistsToShard(t *testing.T) {
	base, client := fakeMCPServer(t)
	wsDir := t.TempDir()
	seedDiscoverServer(t, wsDir, "acme", base)

	// One client (trusting the test cert) governs discovery AND the code exchange.
	auth := workspace.NewMCPAuth(mustMaster(t), wsDir, "main", workspace.WithHTTPClient(client))
	ctx := context.Background()

	const redirect = "http://127.0.0.1:0/callback"
	p, err := auth.Begin(ctx, "acme", []string{"repo"}, redirect)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if p.ID == "" || p.RedirectPrefix != redirect {
		t.Fatalf("PendingAuth = %+v", p)
	}
	u, err := url.Parse(p.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	q := u.Query()
	if q.Get("resource") != base+"/mcp" {
		t.Errorf("resource = %q, want %q", q.Get("resource"), base+"/mcp")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("missing PKCE S256 challenge: %v", q)
	}
	if q.Get("client_id") != "dyn-client-123" {
		t.Errorf("client_id = %q, want the dynamically registered id", q.Get("client_id"))
	}
	state := q.Get("state")

	if err := auth.Complete(ctx, p.ID, "the-auth-code", state); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// The token and the resolved provider record now live in the server's folder shard.
	if _, err := os.Stat(filepath.Join(wsDir, "mcp", "acme", "secrets.enc")); err != nil {
		t.Fatalf("token must land in mcp/acme/secrets.enc: %v", err)
	}
	tokens := workspace.NewShardTokens(mustMaster(t), wsDir, "main", nil)
	sn := "mcp:acme@" + mustHost(t, base) + "/oauth"
	if _, ok := tokens.Get(sn); !ok {
		t.Errorf("token not stored under %q", sn)
	}
	rec, ok := workspace.LoadOAuthRecord(tokens, sn)
	if !ok || rec.ClientID != "dyn-client-123" || rec.Resource != base+"/mcp" {
		t.Errorf("provider record = %+v, ok=%v", rec, ok)
	}
}

// Complete is fail-closed on its guards: an unknown/used session id, and a state that does not match
// the one Begin stashed (a forged callback), both refuse — and neither leaves a token behind.
func TestMCPAuth_Complete_Guards(t *testing.T) {
	base, client := fakeMCPServer(t)
	wsDir := t.TempDir()
	seedDiscoverServer(t, wsDir, "acme", base)
	auth := workspace.NewMCPAuth(mustMaster(t), wsDir, "main", workspace.WithHTTPClient(client))
	ctx := context.Background()

	if err := auth.Complete(ctx, "no-such-id", "code", "state"); err == nil {
		t.Error("Complete of an unknown session must error")
	}

	p, err := auth.Begin(ctx, "acme", []string{"repo"}, "http://127.0.0.1:0/callback")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := auth.Complete(ctx, p.ID, "code", "forged-state"); err == nil {
		t.Error("Complete with a mismatched state must error (forged callback)")
	}
	// The session was consumed even on the failed attempt — a replay finds nothing.
	if err := auth.Complete(ctx, p.ID, "code", "forged-state"); err == nil {
		t.Error("a consumed session must not be replayable")
	}
	if _, err := os.Stat(filepath.Join(wsDir, "mcp", "acme", "secrets.enc")); !os.IsNotExist(err) {
		t.Errorf("no token must be written on a guard failure, stat err = %v", err)
	}
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
