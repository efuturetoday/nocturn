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

func TestResolveSecretTarget(t *testing.T) {
	wsDir := t.TempDir()
	// An MCP server so the host-bound key can be derived.
	mcpGithub := filepath.Join(wsDir, "mcp", "github")
	if err := os.MkdirAll(mcpGithub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mcpGithub, "mcp.json"), []byte(`{"url":"https://api.githubcopilot.com/mcp/","auth":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("plugin", func(t *testing.T) {
		rel, key, err := resolveSecretTarget(wsDir, "plugin:gmail/acct")
		if err != nil || rel != "plugins/gmail" || key != "plugin:gmail/acct" {
			t.Fatalf("rel=%q key=%q err=%v", rel, key, err)
		}
	})
	t.Run("mcp derives host-bound key", func(t *testing.T) {
		rel, key, err := resolveSecretTarget(wsDir, "mcp:github")
		if err != nil || rel != "mcp/github" || key != "mcp:github@api.githubcopilot.com/oauth" {
			t.Fatalf("rel=%q key=%q err=%v", rel, key, err)
		}
	})

	bad := []string{"plugin:gmail", "plugin:Bad Name/acct", "mcp:nope", "mcp:Bad Name", "nonsense", "plugin:/acct"}
	for _, target := range bad {
		if _, _, err := resolveSecretTarget(wsDir, target); err == nil {
			t.Errorf("resolveSecretTarget(%q) = nil error, want an error", target)
		}
	}
}
