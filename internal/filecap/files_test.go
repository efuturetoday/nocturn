package filecap_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/filecap"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// allowGuard permits the given (capability, target-glob) pairs and nothing else
// (deny-by-default), with no HITL — so a denied call surfaces as ErrDenied
// without needing a notifier.
func allowGuard(rules ...capability.Rule) *gateway.Guard {
	return &gateway.Guard{Policy: capability.Policy{Rules: rules}}
}

func allowAll() *gateway.Guard {
	return allowGuard(
		capability.Rule{Family: "file", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent},
	)
}

func toolByName(tools []tool.Tool, name string) tool.Tool {
	for _, t := range tools {
		if t.Name == name {
			return t
		}
	}
	panic("no tool " + name)
}

func TestFiles_ReadWriteRoundtrip(t *testing.T) {
	root := t.TempDir()
	tools := filecap.New(allowAll(), root).Tools()
	write, read := toolByName(tools, "file.write"), toolByName(tools, "file.read")

	if _, err := write.Invoke(context.Background(), `{"path":"notes/todo.md","content":"buy milk"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The file really landed under the workspace at the requested relative path.
	if b, err := os.ReadFile(filepath.Join(root, "notes", "todo.md")); err != nil || string(b) != "buy milk" {
		t.Fatalf("on-disk = %q, %v", b, err)
	}
	out, err := read.Invoke(context.Background(), `{"path":"notes/todo.md"}`)
	if err != nil || out != "buy milk" {
		t.Fatalf("read = %q, %v", out, err)
	}
}

// A path that escapes the workspace is a hard error BEFORE the broker — and
// nothing is written outside root.
func TestFiles_ConfinesToWorkspace(t *testing.T) {
	root := t.TempDir()
	write := toolByName(filecap.New(allowAll(), root).Tools(), "file.write")

	for _, escape := range []string{"../escape.txt", "../../etc/pwn", "notes/../../escape"} {
		if _, err := write.Invoke(context.Background(), `{"path":"`+escape+`","content":"x"}`); err == nil ||
			!strings.Contains(err.Error(), "escapes the workspace") {
			t.Fatalf("escape %q: err = %v, want workspace-escape error", escape, err)
		}
	}
	// The parent of root must be untouched.
	if entries, _ := os.ReadDir(filepath.Dir(root)); len(entries) != 1 { // only root itself
		t.Fatalf("something was written outside the workspace: %v", entries)
	}
}

// The proof that (capability, target=PATH) works with path-glob semantics: a
// ceiling-style policy file.write @ notes/* allows notes/todo.md but denies a
// different directory AND a deeper nesting (path.Match's "*" does not cross "/").
// This is exactly what would have been impossible when target was hard-coded to a
// host.
func TestFiles_TargetIsPathGlobMatched(t *testing.T) {
	root := t.TempDir()
	guard := allowGuard(capability.Rule{
		Family: "file", TargetGlob: "notes/*", Writes: capability.MatchWrite, Effect: capability.Allow, Epoch: capability.Permanent,
	})
	write := toolByName(filecap.New(guard, root).Tools(), "file.write")

	cases := []struct {
		path    string
		allowed bool
	}{
		{"notes/todo.md", true},
		{"secrets/key.txt", false},      // different directory → outside notes/*
		{"notes/deep/nested.md", false}, // deeper → "*" does not cross "/"
		{"notes", false},                // the dir itself is not notes/<something>
	}
	for _, tc := range cases {
		_, err := write.Invoke(context.Background(), `{"path":"`+tc.path+`","content":"x"}`)
		denied := err == gateway.ErrDenied
		if tc.allowed && err != nil {
			t.Errorf("%s: want allowed, got %v", tc.path, err)
		}
		if !tc.allowed && !denied {
			t.Errorf("%s: want ErrDenied, got %v", tc.path, err)
		}
	}
}
