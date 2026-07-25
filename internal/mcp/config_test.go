package mcp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/mcp"
)

// writeServer writes <dir>/<name>/mcp.json — one folder per server.
func writeServer(t *testing.T, dir, name, content string) {
	t.Helper()
	folder := filepath.Join(dir, name)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "mcp.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover_Valid(t *testing.T) {
	dir := t.TempDir()
	writeServer(t, dir, "github", `{"url":"https://mcp.github.com/mcp"}`)
	writeServer(t, dir, "cal", `{"url":"https://cal.example.com/mcp","oauth":{
		"auth_url":"https://auth.example.com/authorize","token_url":"https://auth.example.com/token",
		"client_id":"abc","scopes":["calendar.read"]}}`)

	var diag agentkit.Diagnostics
	set := mcp.Discover(dir, &diag)
	if len(set) != 2 || diag.Len() != 0 {
		t.Fatalf("servers = %+v, diags = %v", set.All(), diag.All())
	}
	// The name is the folder name; a JSON "name" may override it.
	gh, ok := set.Get("github")
	if !ok || gh.URL != "https://mcp.github.com/mcp" {
		t.Fatalf("github parsed wrong: %+v", gh)
	}
	cal, _ := set.Get("cal")
	if cal.OAuth == nil || cal.OAuth.ClientID != "abc" {
		t.Fatalf("cal oauth parsed wrong: %+v", cal)
	}
}

// A missing dir means "no servers", not an error.
func TestDiscover_MissingDir(t *testing.T) {
	var diag agentkit.Diagnostics
	set := mcp.Discover(filepath.Join(t.TempDir(), "absent"), &diag)
	if len(set) != 0 || diag.Len() != 0 {
		t.Fatalf("missing dir yielded %d servers, %d diags", len(set), diag.Len())
	}
}

// A nil collector is tolerated (the OAuth aggregator discovers without one).
func TestDiscover_NilCollector(t *testing.T) {
	dir := t.TempDir()
	writeServer(t, dir, "github", `{"url":"https://mcp.github.com/mcp"}`)
	if set := mcp.Discover(dir, nil); len(set) != 1 {
		t.Fatalf("nil collector must still discover: %+v", set.All())
	}
}

// The static-token/OAuth matrix: either one alone (or neither) is valid;
// both together are ambiguous and rejected fail-closed.
func TestServer_Validate_TokenOAuthMatrix(t *testing.T) {
	oauth := &mcp.OAuthDecl{
		AuthURL: "https://a.example.com", TokenURL: "https://t.example.com",
		ClientID: "c", Scopes: []string{"s"},
	}
	cases := []struct {
		label string
		srv   mcp.Server
		ok    bool
	}{
		{"neither", mcp.Server{Name: "a", URL: "https://x.example.com"}, true},
		{"token only", mcp.Server{Name: "a", URL: "https://x.example.com", Auth: "token"}, true},
		{"oauth only", mcp.Server{Name: "a", URL: "https://x.example.com", OAuth: oauth}, true},
		{"both", mcp.Server{Name: "a", URL: "https://x.example.com", Auth: "token", OAuth: oauth}, false},
		{"bad auth", mcp.Server{Name: "a", URL: "https://x.example.com", Auth: "env"}, false},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			if err := c.srv.Validate(); (err == nil) != c.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

// A malformed server manifest is SKIPPED with an Error diagnostic — never aborts the
// scan, never half-loads (its tools + token wiring are simply absent). The other
// servers in the same dir still load.
func TestDiscover_MalformedSkipped(t *testing.T) {
	cases := map[string]string{
		"http url":        `{"url":"http://mcp.example.com/mcp"}`,
		"no url":          `{}`,
		"unknown field":   `{"url":"https://x.example.com","exec":"/bin/sh"}`,
		"oauth no client": `{"url":"https://x.example.com","oauth":{"auth_url":"https://a.example.com","token_url":"https://t.example.com","scopes":["s"]}}`,
		"token and oauth": `{"url":"https://x.example.com","auth":"token","oauth":{"auth_url":"https://a.example.com","token_url":"https://t.example.com","client_id":"c","scopes":["s"]}}`,
		"bad auth mode":   `{"url":"https://x.example.com","auth":"env"}`,
		"oauth http":      `{"url":"https://x.example.com","oauth":{"auth_url":"http://a.example.com","token_url":"https://t.example.com","client_id":"c","scopes":["s"]}}`,
		"not json":        `servers: [yaml]`,
	}
	for label, content := range cases {
		t.Run(label, func(t *testing.T) {
			dir := t.TempDir()
			writeServer(t, dir, "bad", content)
			writeServer(t, dir, "good", `{"url":"https://good.example.com/mcp"}`)
			var diag agentkit.Diagnostics
			set := mcp.Discover(dir, &diag)
			if _, ok := set.Get("good"); !ok || len(set) != 1 {
				t.Fatalf("a malformed server must not stop the good one: %+v", set.All())
			}
			if diag.Len() != 1 {
				t.Fatalf("want 1 diagnostic for %s, got %v", label, diag.All())
			}
		})
	}
}

// The folder is the identity; a "name" field is advisory only (the folder wins,
// with a mismatch warning) — so a manifest cannot forge the credential owner.
func TestDiscover_FolderWinsOverNameField(t *testing.T) {
	dir := t.TempDir()
	writeServer(t, dir, "github", `{"name":"other","url":"https://mcp.github.com/mcp"}`)
	var diag agentkit.Diagnostics
	set := mcp.Discover(dir, &diag)
	if _, ok := set.Get("github"); !ok || len(set) != 1 {
		t.Fatalf("the folder must win over the name field: %+v", set.All())
	}
	if _, ok := set.Get("other"); ok {
		t.Fatalf("the name field must NOT become the identity")
	}
	if diag.Len() != 1 {
		t.Fatalf("a name/folder mismatch must warn, got %v", diag.All())
	}
}

// A folder name that is not a valid server name is skipped (Validate checks the resolved name).
func TestDiscover_BadFilenameSkipped(t *testing.T) {
	dir := t.TempDir()
	writeServer(t, dir, "Bad Name!", `{"url":"https://x.example.com/mcp"}`)
	var diag agentkit.Diagnostics
	set := mcp.Discover(dir, &diag)
	if len(set) != 0 || diag.Len() != 1 {
		t.Fatalf("a bad folder name must be skipped: %+v diags=%v", set.All(), diag.All())
	}
}

func TestOwnerAndSecretName(t *testing.T) {
	if got := mcp.Owner("github"); got != "mcp:github" {
		t.Errorf("Owner = %q", got)
	}
	// The secret is bound to (name, host): the SAME server name at a DIFFERENT
	// host yields a different key, so a stored token can never ride to a new host.
	if got := mcp.SecretName("github", "api.githubcopilot.com"); got != "mcp:github@api.githubcopilot.com/oauth" {
		t.Errorf("SecretName = %q", got)
	}
	if a, b := mcp.SecretName("github", "api.githubcopilot.com"), mcp.SecretName("github", "evil.com"); a == b {
		t.Errorf("same-name/other-host keys must differ: %q == %q", a, b)
	}
	// Host is lowercased so the key stays stable across case.
	if got := mcp.SecretName("github", "API.GitHub.COM"); got != "mcp:github@api.github.com/oauth" {
		t.Errorf("SecretName host not lowercased: %q", got)
	}
	// The typed prefix keeps an MCP server "github" and a plugin "github" in
	// distinct owner namespaces — no credential can cross.
	if mcp.Owner("github") == "plugin:github" {
		t.Error("owner namespaces collide")
	}
}

// The three OAuth-bearing modes are exclusive and classify correctly: a static token,
// spec discovery (auth:"oauth", no block), and a manual endpoints block.
func TestServer_OAuthMode(t *testing.T) {
	const u = "https://x.example.com"
	block := &mcp.OAuthDecl{AuthURL: "https://a.example.com", TokenURL: "https://t.example.com", ClientID: "c", Scopes: []string{"s"}}

	discover := mcp.Server{Name: "a", URL: u, Auth: "oauth"}
	if err := discover.Validate(); err != nil {
		t.Errorf("discover mode (auth:oauth, no block) must validate: %v", err)
	}
	if discover.OAuthMode() != mcp.AuthDiscover {
		t.Errorf("mode = %q, want discover", discover.OAuthMode())
	}
	if bad := (mcp.Server{Name: "a", URL: u, Auth: "oauth", OAuth: block}); bad.Validate() == nil {
		t.Error("auth:oauth WITH a manual block must be rejected (exclusive)")
	}
	if m := (mcp.Server{Name: "a", URL: u, OAuth: block}); m.OAuthMode() != mcp.AuthManual {
		t.Errorf("manual mode = %q", m.OAuthMode())
	}
	if tk := (mcp.Server{Name: "a", URL: u, Auth: "token"}); tk.OAuthMode() != mcp.AuthToken {
		t.Errorf("token mode = %q", tk.OAuthMode())
	}
	if none := (mcp.Server{Name: "a", URL: u}); none.OAuthMode() != mcp.AuthNone {
		t.Errorf("none mode = %q", none.OAuthMode())
	}
}
