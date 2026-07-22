package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/agent"
)

// writeAgent creates dir/<name>/agent.md with the given content.
func writeAgent(t *testing.T, dir, name, content string) {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write agent.md: %v", err)
	}
}

func TestDiscover_ReadsAgentMd_PerSubdir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeAgent(t, dir, "researcher", "---\nname: researcher\ntools:\n  - http\n  - file_read\nwhen: \"*/5 * * * *\"\neffort: high\n---\nYou are a researcher.\nDig deep.\n")
	writeAgent(t, dir, "greeter", "---\nname: greeter\n---\nSay hello.\n")

	set, err := agent.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(set) != 2 {
		t.Fatalf("Discover found %d agents, want 2: %v", len(set), set)
	}

	r, ok := set.Get("researcher")
	if !ok {
		t.Fatal("researcher not discovered")
	}
	if r.When != "*/5 * * * *" {
		t.Errorf("researcher When = %q, want %q", r.When, "*/5 * * * *")
	}
	if r.Effort != agentkit.Effort("high") {
		t.Errorf("researcher Effort = %q, want high", r.Effort)
	}
	if want := "You are a researcher.\nDig deep."; r.Instructions != want {
		t.Errorf("researcher Instructions = %q, want %q", r.Instructions, want)
	}
	if len(r.Tools) != 2 || r.Tools[0] != "http" || r.Tools[1] != "file_read" {
		t.Errorf("researcher Tools = %v, want [http file_read]", r.Tools)
	}

	g, ok := set.Get("greeter")
	if !ok {
		t.Fatal("greeter not discovered")
	}
	if g.When != "" {
		t.Errorf("greeter When = %q, want empty (manual only)", g.When)
	}
}

func TestDiscover_MissingDir_EmptySet(t *testing.T) {
	t.Parallel()

	set, err := agent.Discover(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("Discover on missing dir returned error: %v", err)
	}
	if len(set) != 0 {
		t.Errorf("Discover on missing dir = %v, want empty set", set)
	}
}

func TestDiscover_NonDirEntries_Skipped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeAgent(t, dir, "real", "---\nname: real\n---\nbody\n")
	// A loose file at the root is not a subdir → skipped.
	if err := os.WriteFile(filepath.Join(dir, "loose.md"), []byte("not an agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A subdir without an agent.md yields (nil, nil) from loadAgent → skipped.
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	set, err := agent.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(set) != 1 {
		t.Fatalf("Discover = %d agents, want 1 (only 'real'): %v", len(set), set)
	}
	if _, ok := set.Get("real"); !ok {
		t.Error("expected 'real' agent")
	}
}

func TestDiscover_MissingName_PropagatesError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeAgent(t, dir, "broken", "---\ntools:\n  - http\n---\nno name here\n")

	if _, err := agent.Discover(dir); err == nil {
		t.Fatal("Discover with a nameless agent.md = nil error, want error")
	}
}
