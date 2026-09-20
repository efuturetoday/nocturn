package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// parseArgs collects positionals whether flags come before, after, or around them —
// stdlib flag alone would drop a flag placed after the first positional.
func TestParseArgs_Interspersed(t *testing.T) {
	cases := map[string][]string{
		"flag before": {"-w", "main", "plugin:gmail/acct"},
		"flag after":  {"plugin:gmail/acct", "-w", "main"},
		"eq form":     {"plugin:gmail/acct", "-w=main"},
		"no flag":     {"plugin:gmail/acct"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			ws := fs.String("w", "def", "")
			pos, code, done := parseArgs(fs, args)
			if done {
				t.Fatalf("unexpected stop (code %d)", code)
			}
			if len(pos) != 1 || pos[0] != "plugin:gmail/acct" {
				t.Fatalf("positionals = %v, want [plugin:gmail/acct]", pos)
			}
			if name != "no flag" && *ws != "main" {
				t.Errorf("workspace = %q, want main", *ws)
			}
		})
	}
}

// TestResolveSecretTarget: one grammar for every kind. The reference names an installed extension,
// the key comes from that extension's own declaration — so a target that names nothing installed is
// refused rather than resolving to a key nothing would ever look up.
func TestResolveSecretTarget(t *testing.T) {
	wsDir := t.TempDir()

	// An MCP server so the host-bound key can be derived.
	mcpGithub := filepath.Join(wsDir, "extensions", "github")
	if err := os.MkdirAll(mcpGithub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mcpGithub, "mcp.json"), []byte(`{"url":"https://api.githubcopilot.com/mcp/","auth":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// A plugin declaring one credential.
	pluginGmail := filepath.Join(wsDir, "extensions", "gmail")
	if err := os.MkdirAll(pluginGmail, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginGmail, "plugin.json"), []byte(`{
		"name":"gmail","version":"1",
		"tools":[{"name":"send","parameters":{"type":"object"}}],
		"uses":["http_write"],
		"credentials":[{"name":"acct","host":"gmail.googleapis.com","header":"Authorization","prefix":"Bearer "}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginGmail, "plugin.js"), []byte("// stub"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A skill declaring a credential whose host comes from its config.
	skillHouse := filepath.Join(wsDir, "extensions", "house")
	if err := os.MkdirAll(skillHouse, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillHouse, "manifest.json"), []byte(`{
		"config":[{"name":"base_url","type":"url"}],
		"credentials":[{"name":"token","host":"{{config.base_url}}","header":"Authorization","prefix":"Bearer "}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillHouse, "config.json"), []byte(`{"base_url":"https://hass.example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct{ target, rel, key string }{
		"a named credential":            {"gmail/acct", "extensions/gmail", "ext:gmail@gmail.googleapis.com/acct"},
		"the only credential":           {"gmail", "extensions/gmail", "ext:gmail@gmail.googleapis.com/acct"},
		"a server's bearer":             {"github", "extensions/github", "ext:github@api.githubcopilot.com/oauth"},
		"a host that comes from config": {"house/token", "extensions/house", "ext:house@hass.example.com/token"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rel, key, err := resolveSecretTarget(wsDir, tc.target)
			if err != nil || rel != tc.rel || key != tc.key {
				t.Fatalf("rel=%q key=%q err=%v; want rel=%q key=%q", rel, key, err, tc.rel, tc.key)
			}
		})
	}

	// A skill with no address configured yet has no host to bind to, so there is no key to seed —
	// better refused here than stored under a name nothing will ever look up.
	if err := os.Remove(filepath.Join(skillHouse, "config.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveSecretTarget(wsDir, "house/token"); err == nil {
		t.Error("an unconfigured skill resolved to a credential key")
	}

	bad := []string{"Bad Name/acct", "nope", "Bad Name", "/acct", "missing/token"}
	for _, target := range bad {
		if _, _, err := resolveSecretTarget(wsDir, target); err == nil {
			t.Errorf("resolveSecretTarget(%q) = nil error, want an error", target)
		}
	}
}
