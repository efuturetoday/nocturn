package mcpcap_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/mcpcap"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig_Valid(t *testing.T) {
	path := writeConfig(t, `{"servers":[
		{"name":"github","url":"https://mcp.github.com/mcp"},
		{"name":"cal","url":"https://cal.example.com/mcp","oauth":{
			"auth_url":"https://auth.example.com/authorize","token_url":"https://auth.example.com/token",
			"client_id":"abc","scopes":["calendar.read"]}}
	]}`)
	servers, err := mcpcap.LoadConfig(path)
	if err != nil || len(servers) != 2 {
		t.Fatalf("servers = %+v, err=%v", servers, err)
	}
	if servers[0].Name != "github" || servers[1].OAuth == nil || servers[1].OAuth.ClientID != "abc" {
		t.Fatalf("parsed wrong: %+v", servers)
	}
}

// A missing config file means "no servers", not an error — the assistant runs
// fine without any MCP configured.
func TestLoadConfig_MissingFile(t *testing.T) {
	servers, err := mcpcap.LoadConfig(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || servers != nil {
		t.Fatalf("servers=%v err=%v, want nil,nil", servers, err)
	}
}

func TestLoadConfig_FailClosed(t *testing.T) {
	cases := map[string]string{
		"http url":        `{"servers":[{"name":"a","url":"http://mcp.example.com/mcp"}]}`,
		"no url":          `{"servers":[{"name":"a"}]}`,
		"bad name":        `{"servers":[{"name":"Bad Name!","url":"https://x.example.com"}]}`,
		"duplicate name":  `{"servers":[{"name":"a","url":"https://x.example.com"},{"name":"a","url":"https://y.example.com"}]}`,
		"unknown field":   `{"servers":[{"name":"a","url":"https://x.example.com","exec":"/bin/sh"}]}`,
		"oauth no client": `{"servers":[{"name":"a","url":"https://x.example.com","oauth":{"auth_url":"https://a.example.com","token_url":"https://t.example.com","scopes":["s"]}}]}`,
		"oauth http":      `{"servers":[{"name":"a","url":"https://x.example.com","oauth":{"auth_url":"http://a.example.com","token_url":"https://t.example.com","client_id":"c","scopes":["s"]}}]}`,
		"not json":        `servers: [yaml]`,
	}
	for label, content := range cases {
		t.Run(label, func(t *testing.T) {
			if _, err := mcpcap.LoadConfig(writeConfig(t, content)); err == nil {
				t.Fatalf("LoadConfig accepted a config with %s", label)
			}
		})
	}
}

func TestOwnerAndSecretName(t *testing.T) {
	if got := mcpcap.Owner("github"); got != "mcp:github" {
		t.Errorf("Owner = %q", got)
	}
	if got := mcpcap.SecretName(mcpcap.Owner("github"), mcpcap.CredentialName); got != "mcp:github/oauth" {
		t.Errorf("SecretName = %q", got)
	}
	// The typed prefix keeps an MCP server "github" and a plugin "github" in
	// distinct owner namespaces — no credential can cross.
	if mcpcap.Owner("github") == "plugin:github" {
		t.Error("owner namespaces collide")
	}
}
