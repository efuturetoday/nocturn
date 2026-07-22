package mcp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/app/mcp"
)

// DiscoverOAuth returns exactly the OAuth-declaring servers, with the host-bound vault key that
// NewConn's binding names (host WITH port), and copies the endpoints/scopes. token-auth and public
// servers contribute nothing.
func TestDiscoverOAuth(t *testing.T) {
	wsDir := t.TempDir()
	// One OAuth server (on an explicit port, to guard the host-with-port invariant), one token
	// server, one public — only the first is an OAuth provider.
	cfg := `{"servers":[
		{"name":"cal","url":"https://cal.example.com:8443/mcp","oauth":{
			"auth_url":"https://auth.example.com/authorize","token_url":"https://auth.example.com/token",
			"client_id":"abc","client_secret":"shh","scopes":["calendar.read","calendar.write"]}},
		{"name":"tok","url":"https://tok.example.com/mcp","auth":"token"},
		{"name":"pub","url":"https://pub.example.com/mcp"}
	]}`
	if err := os.WriteFile(filepath.Join(wsDir, "mcp.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	got := mcp.DiscoverOAuth(wsDir)
	if len(got) != 1 {
		t.Fatalf("DiscoverOAuth = %d providers, want 1 (only the oauth server)", len(got))
	}
	p := got[0]
	if p.Name != "cal" {
		t.Errorf("Name = %q, want cal", p.Name)
	}
	// The vault key must equal what NewConn binds: SecretName(name, u.Host) with the port.
	if want := mcp.SecretName("cal", "cal.example.com:8443"); p.SecretName != want {
		t.Errorf("SecretName = %q, want %q", p.SecretName, want)
	}
	if p.AuthURL != "https://auth.example.com/authorize" || p.TokenURL != "https://auth.example.com/token" {
		t.Errorf("endpoints not copied: %+v", p)
	}
	if p.ClientID != "abc" || p.ClientSecret != "shh" || len(p.Scopes) != 2 {
		t.Errorf("client/scopes not copied: %+v", p)
	}
}

// A root with no mcp.json (or no workspaces) yields no providers and never panics.
func TestDiscoverOAuth_Empty(t *testing.T) {
	if got := mcp.DiscoverOAuth(t.TempDir()); len(got) != 0 {
		t.Fatalf("want no providers, got %+v", got)
	}
	// A nonexistent root is also fine.
	if got := mcp.DiscoverOAuth(filepath.Join(t.TempDir(), "absent")); len(got) != 0 {
		t.Fatalf("want no providers for absent root, got %+v", got)
	}
}
