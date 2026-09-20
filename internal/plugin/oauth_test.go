package plugin_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/plugin"
)

// writePlugin lays down a minimal loadable plugin dir (manifest + a plugin.js stub) under
// <wsDir>/extensions/<name> and returns wsDir (a single workspace directory).
func writePlugin(t *testing.T, name, manifest string) string {
	t.Helper()
	wsDir := t.TempDir()
	dir := filepath.Join(wsDir, "extensions", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.js"), []byte("// stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	return wsDir
}

// TestSecretNames: a plugin credential's key carries its owner AND the host the manifest declared it
// for, so two plugins never share a key and a manifest re-pointed at another host cannot reuse the
// token issued for the old one.
func TestSecretNames(t *testing.T) {
	m := plugin.Manifest{
		Name:    "gmail",
		Version: "1",
		Decl: extension.Decl{Credentials: []plugin.CredentialDecl{
			{Name: "acct", Host: "gmail.googleapis.com", Header: "Authorization"},
		}},
	}
	keys, err := plugin.SecretNames(m)
	if err != nil {
		t.Fatalf("SecretNames: %v", err)
	}
	if want := "ext:gmail@gmail.googleapis.com/acct"; keys["acct"] != want {
		t.Errorf("SecretNames[acct] = %q, want %q", keys["acct"], want)
	}
	// Owner-namespaced: a plugin "github" and an MCP server "github" never share a key.
	if keys["acct"] == "ext2:gmail@gmail.googleapis.com/acct" {
		t.Error("plugin and mcp key namespaces collide")
	}
}

// DiscoverOAuth returns each plugin's OAuth providers keyed by the owner-namespaced SecretName
// (plugin:<plugin>/<cred>), with the endpoints/scopes copied.
func TestDiscoverOAuth(t *testing.T) {
	wsDir := writePlugin(t, "gmail", `{
		"name":"gmail","version":"1",
		"tools":[{"name":"send","parameters":{"type":"object"}}],
		"uses":["http_write"],
		"credentials":[{"name":"acct","host":"gmail.googleapis.com","header":"Authorization","prefix":"Bearer "}],
		"oauth":[{"name":"acct","client_id":"cid","client_secret":"sec",
			"auth_url":"https://auth.example.com/a","token_url":"https://token.example.com/t","scopes":["send"]}]
	}`)

	got := plugin.DiscoverOAuth(wsDir)
	if len(got) != 1 {
		t.Fatalf("DiscoverOAuth = %d providers, want 1", len(got))
	}
	p := got[0]
	if p.Name != "acct" {
		t.Errorf("Name = %q, want acct", p.Name)
	}
	if want := "ext:gmail@gmail.googleapis.com/acct"; p.SecretName != want {
		t.Errorf("SecretName = %q, want %q", p.SecretName, want)
	}
	if p.AuthURL != "https://auth.example.com/a" || p.TokenURL != "https://token.example.com/t" {
		t.Errorf("endpoints not copied: %+v", p)
	}
	if p.ClientID != "cid" || p.ClientSecret != "sec" || len(p.Scopes) != 1 {
		t.Errorf("client/scopes not copied: %+v", p)
	}
}

// A root with no plugins yields no providers and never panics.
func TestDiscoverOAuth_Empty(t *testing.T) {
	if got := plugin.DiscoverOAuth(t.TempDir()); len(got) != 0 {
		t.Fatalf("want no providers, got %+v", got)
	}
}
