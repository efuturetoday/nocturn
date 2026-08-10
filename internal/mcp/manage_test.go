package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/mcp"
)

// A declaration that Discover would skip is refused at write time instead. A server that silently
// never appears is the one failure a person cannot act on.
func TestWrite_RefusesWhatDiscoverWouldSkip(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []mcp.Server{
		{Name: "acme", URL: "http://acme.example/mcp"},                     // not https
		{Name: "Acme", URL: "https://acme.example/mcp"},                    // invalid name
		{Name: "acme", URL: "https://acme.example/mcp", Auth: "sometimes"}, // unknown auth
		{Name: "acme", URL: "https://acme.example/mcp", Auth: "token",
			OAuth: &mcp.OAuthDecl{AuthURL: "https://a", TokenURL: "https://t", ClientID: "c", Scopes: []string{"s"}}},
	} {
		if err := mcp.Write(dir, s); err == nil {
			t.Fatalf("accepted an invalid declaration: %+v", s)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused declaration still left something on disk: %v", entries)
	}
}

// What Write puts down is what Discover reads back — the folder is the name, so the file does not
// repeat it.
func TestWrite_IsDiscoveredBack(t *testing.T) {
	dir := t.TempDir()
	if err := mcp.Write(dir, mcp.Server{Name: "acme", URL: "https://acme.example/mcp", Auth: "token"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "acme", mcp.ConfigFile))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, repeated := raw["name"]; repeated {
		t.Error("the file repeats the name the folder already carries")
	}

	set := mcp.Discover(dir, nil)
	srv, ok := set["acme"]
	if !ok {
		t.Fatal("the written server is not discovered")
	}
	if srv.URL != "https://acme.example/mcp" || srv.Auth != "token" {
		t.Fatalf("discovered %+v", srv)
	}
}

// Overwriting under the same name would keep the folder — and its secret shard, keyed by that
// folder's path — while pointing a credential authorized for one host at another.
func TestWrite_RefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := mcp.Write(dir, mcp.Server{Name: "acme", URL: "https://acme.example/mcp"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.Write(dir, mcp.Server{Name: "acme", URL: "https://evil.example/mcp"}); err == nil {
		t.Fatal("a second declaration overwrote the first")
	}
}

// Removing takes the shard with it. The shard is encrypted under a key derived from this folder's
// path, so it is readable nowhere else — leaving it would be a token for a server nobody declares.
func TestRemove_TakesTheShard(t *testing.T) {
	dir := t.TempDir()
	if err := mcp.Write(dir, mcp.Server{Name: "acme", URL: "https://acme.example/mcp"}); err != nil {
		t.Fatal(err)
	}
	shard := filepath.Join(dir, "acme", "secrets.enc")
	if err := os.WriteFile(shard, []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := mcp.Remove(dir, "acme"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "acme")); !os.IsNotExist(err) {
		t.Error("the folder survived")
	}
	if err := mcp.Remove(dir, "acme"); err == nil {
		t.Error("removing a server that is not there reported success")
	}
}

// Host is what a server's gate grants are keyed by, so a caller can revoke them.
func TestHost(t *testing.T) {
	if h, ok := mcp.Host("https://api.example.com:8443/mcp"); !ok || h != "api.example.com:8443" {
		t.Fatalf("Host = %q, %v", h, ok)
	}
	if _, ok := mcp.Host("not a url"); ok {
		t.Error("a URL with no host was accepted")
	}
}
