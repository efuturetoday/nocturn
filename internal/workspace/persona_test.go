package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// loadPersona resolves the session system prompt with OVERRIDE semantics across two
// layers plus a built-in default: the workspace's own PERSONA.md wins over the shared
// one in the parent directory, which wins over defaultPersona. Each layer is a COMPLETE
// persona — never appended.
func TestLoadPersona_LayeredOverride(t *testing.T) {
	root := t.TempDir()                  // stands in for workspaces/
	wsDir := filepath.Join(root, "myws") // workspaces/myws
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(dir, body string) {
		if err := os.WriteFile(filepath.Join(dir, "PERSONA.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// No file at either layer → the built-in default.
	if got := loadPersona(wsDir); got != defaultPersona {
		t.Fatalf("no PERSONA.md: got %q, want the built-in default", got)
	}

	// Only the shared (parent) layer → it is used by every workspace.
	write(root, "shared persona")
	if got := loadPersona(wsDir); got != "shared persona" {
		t.Fatalf("shared layer: got %q, want %q", got, "shared persona")
	}

	// The workspace's OWN PERSONA.md overrides the shared one (replace, not append).
	write(wsDir, "  my own persona\n")
	if got := loadPersona(wsDir); got != "my own persona" {
		t.Fatalf("workspace override: got %q, want the trimmed own persona (no shared text)", got)
	}

	// A blank own file falls through to the shared layer (an empty override is not a
	// persona — it must not silently mute the assistant's identity).
	write(wsDir, "   \n\t\n")
	if got := loadPersona(wsDir); got != "shared persona" {
		t.Fatalf("blank own file: got %q, want fallthrough to the shared layer", got)
	}
}
