package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/app/mcp"
	"github.com/efuturetoday/nocturn/app/plugin"
	"github.com/efuturetoday/nocturn/app/workspace"
)

// TestDiscoverOAuth_AggregatesSources: a workspace dir with a plugin OAuth provider AND an MCP OAuth
// server yields both, each under its own owner-namespaced vault key — the plugin under
// plugin.SecretName, the MCP server under mcp.SecretName (host-bound).
func TestDiscoverOAuth_AggregatesSources(t *testing.T) {
	wsDir := t.TempDir()

	// A plugin with an OAuth credential.
	pdir := filepath.Join(wsDir, "plugins", "gmail")
	if err := os.MkdirAll(pdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "plugin.json"), []byte(`{
		"name":"gmail","version":"1",
		"tools":[{"name":"send","parameters":{"type":"object"}}],
		"uses":["http_write"],
		"credentials":[{"name":"acct","host":"gmail.googleapis.com","header":"Authorization","prefix":"Bearer "}],
		"oauth":[{"name":"acct","client_id":"c","auth_url":"https://a/x","token_url":"https://t/y","scopes":["s"]}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "plugin.js"), []byte("// stub"), 0o600); err != nil {
		t.Fatal(err)
	}

	// An MCP server with an OAuth block — one file per server, name from the filename.
	mcpDir := filepath.Join(wsDir, "mcp")
	if err := os.MkdirAll(mcpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mcpDir, "cal.json"), []byte(`{"url":"https://cal.example.com/mcp","oauth":{
		"auth_url":"https://a/x","token_url":"https://t/y","client_id":"c","scopes":["s"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := workspace.DiscoverOAuth(wsDir)
	keys := map[string]bool{}
	for _, p := range got {
		keys[p.SecretName] = true
	}
	if !keys[plugin.SecretName("gmail", "acct")] {
		t.Errorf("missing plugin provider key %q; got %v", plugin.SecretName("gmail", "acct"), keys)
	}
	if !keys[mcp.SecretName("cal", "cal.example.com")] {
		t.Errorf("missing mcp provider key %q; got %v", mcp.SecretName("cal", "cal.example.com"), keys)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 providers, got %d: %+v", len(got), got)
	}
}
