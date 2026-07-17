package persona_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/persona"
)

func write(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "PERSONA.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Load resolves with OVERRIDE semantics: the workspace's own PERSONA.md wins over the
// shared one in the parent, which wins over the built-in Default. Each layer is complete.
func TestStore_LayeredLoad(t *testing.T) {
	root := t.TempDir()               // stands in for workspaces/
	ws := filepath.Join(root, "myws") // workspaces/myws
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := persona.Load(ws).Get(); got != persona.Default {
		t.Fatalf("no file: got %q, want the built-in Default", got)
	}
	write(t, root, "shared persona")
	if got := persona.Load(ws).Get(); got != "shared persona" {
		t.Fatalf("shared layer: got %q", got)
	}
	write(t, ws, "  my own persona\n")
	if got := persona.Load(ws).Get(); got != "my own persona" {
		t.Fatalf("workspace override: got %q (want the trimmed own persona)", got)
	}
}

// Set persists to the workspace's PERSONA.md, updates the in-memory value, and re-resolves
// — so a blank write falls back to the shared/default layer rather than pinning an empty
// prompt.
func TestStore_SetPersistsAndReresolves(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "myws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "shared persona") // the fallback layer

	s := persona.Load(ws)
	if s.Get() != "shared persona" {
		t.Fatalf("initial: got %q, want the shared fallback", s.Get())
	}

	if err := s.Set("You are Bob."); err != nil {
		t.Fatal(err)
	}
	if s.Get() != "You are Bob." {
		t.Fatalf("after Set: got %q", s.Get())
	}
	if reloaded := persona.Load(ws).Get(); reloaded != "You are Bob." {
		t.Fatalf("persisted: a fresh Load got %q, want the set persona", reloaded)
	}

	// A blank set falls back to the shared layer (an empty override is not a persona).
	if err := s.Set("   \n"); err != nil {
		t.Fatal(err)
	}
	if s.Get() != "shared persona" {
		t.Fatalf("blank Set: got %q, want fallthrough to the shared layer", s.Get())
	}
}
