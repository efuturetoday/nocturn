package filecap_test

import (
	"context"
	"encoding/json"
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

func TestFiles_List(t *testing.T) {
	root := t.TempDir()
	tools := filecap.New(allowAll(), root).Tools()
	write, list := toolByName(tools, "file.write"), toolByName(tools, "file.list")

	mustInvoke(t, write, `{"path":"a.txt","content":"hi"}`)
	mustInvoke(t, write, `{"path":"sub/b.txt","content":"x"}`)

	out := mustInvoke(t, list, `{}`) // default path = workspace root
	var entries []struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("list output %q: %v", out, err)
	}
	got := map[string]bool{} // name -> isDir
	for _, e := range entries {
		got[e.Name] = e.IsDir
	}
	if isDir, ok := got["a.txt"]; !ok || isDir {
		t.Errorf("want a.txt as file, entries=%v", entries)
	}
	if isDir, ok := got["sub"]; !ok || !isDir {
		t.Errorf("want sub as dir, entries=%v", entries)
	}
}

func TestFiles_Stat(t *testing.T) {
	root := t.TempDir()
	tools := filecap.New(allowAll(), root).Tools()
	write, stat := toolByName(tools, "file.write"), toolByName(tools, "file.stat")
	mustInvoke(t, write, `{"path":"notes/todo.md","content":"buy milk"}`)

	cases := []struct {
		path          string
		exists, isDir bool
		size          int64
	}{
		{"notes/todo.md", true, false, 8},
		{"notes", true, true, 0},
		{"nope.txt", false, false, 0},
	}
	for _, tc := range cases {
		out := mustInvoke(t, stat, `{"path":"`+tc.path+`"}`)
		var s struct {
			Exists bool  `json:"exists"`
			IsDir  bool  `json:"isDir"`
			Size   int64 `json:"size"`
		}
		if err := json.Unmarshal([]byte(out), &s); err != nil {
			t.Fatalf("%s: stat output %q: %v", tc.path, out, err)
		}
		if s.Exists != tc.exists || s.IsDir != tc.isDir {
			t.Errorf("%s: got %+v, want exists=%v isDir=%v", tc.path, s, tc.exists, tc.isDir)
		}
		if tc.exists && !tc.isDir && s.Size != tc.size {
			t.Errorf("%s: size = %d, want %d", tc.path, s.Size, tc.size)
		}
	}
}

func TestFiles_Remove(t *testing.T) {
	root := t.TempDir()
	tools := filecap.New(allowAll(), root).Tools()
	write, remove := toolByName(tools, "file.write"), toolByName(tools, "file.remove")
	mustInvoke(t, write, `{"path":"gone.txt","content":"x"}`)

	if out := mustInvoke(t, remove, `{"path":"gone.txt"}`); !strings.Contains(out, "removed gone.txt") {
		t.Fatalf("remove = %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
	// Removing a missing file errors (os.Remove); still no escape possible.
	if _, err := remove.Invoke(context.Background(), `{"path":"gone.txt"}`); err == nil {
		t.Fatalf("second remove: want error, got nil")
	}
	if _, err := remove.Invoke(context.Background(), `{"path":"../escape"}`); err == nil ||
		!strings.Contains(err.Error(), "escapes the workspace") {
		t.Fatalf("escape remove: err = %v, want workspace-escape error", err)
	}
}

// list/stat are reads (Write:false); remove is a write (Write:true). A guard that
// allows only reads runs list/stat silently but denies remove — proving the
// write-axis is what gates the mutation.
func TestFiles_ListStatAreReads_RemoveIsWrite(t *testing.T) {
	root := t.TempDir()
	readOnly := allowGuard(capability.Rule{
		Family: "file", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent,
	})
	tools := filecap.New(readOnly, root).Tools()
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)

	if _, err := toolByName(tools, "file.list").Invoke(context.Background(), `{}`); err != nil {
		t.Errorf("list under read-only: %v", err)
	}
	if _, err := toolByName(tools, "file.stat").Invoke(context.Background(), `{"path":"f.txt"}`); err != nil {
		t.Errorf("stat under read-only: %v", err)
	}
	if _, err := toolByName(tools, "file.remove").Invoke(context.Background(), `{"path":"f.txt"}`); err != gateway.ErrDenied {
		t.Errorf("remove under read-only: err = %v, want ErrDenied", err)
	}
}

func mustInvoke(t *testing.T, tl tool.Tool, args string) string {
	t.Helper()
	out, err := tl.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("%s(%s): %v", tl.Name, args, err)
	}
	return out
}

// The proof that (capability, target=PATH) works with path-glob semantics: a
// cage-style policy file.write @ notes/* allows notes/todo.md but denies a
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
