package main

import (
	"path/filepath"
	"testing"
)

func TestResolveWorkspace(t *testing.T) {
	ok := map[string][]string{ // wantName : args
		"default":  nil,
		"work":     {"work"},
		"p.rivate": {"p.rivate"},
		"a-b_c":    {"a-b_c"},
	}
	for wantName, args := range ok {
		name, dir, err := resolveWorkspace(args)
		if err != nil {
			t.Errorf("resolveWorkspace(%v): unexpected error %v", args, err)
			continue
		}
		if name != wantName {
			t.Errorf("resolveWorkspace(%v) name = %q, want %q", args, name, wantName)
		}
		if dir != filepath.Join("workspaces", wantName) {
			t.Errorf("resolveWorkspace(%v) dir = %q", args, dir)
		}
	}

	// Empty / whitespace arg → default.
	if name, _, err := resolveWorkspace([]string{"  "}); err != nil || name != "default" {
		t.Errorf("blank arg: name=%q err=%v, want default/nil", name, err)
	}

	// Names that could escape the workspaces/ dir or are otherwise unsafe are rejected.
	for _, bad := range []string{"../etc", "a/b", "/abs", "..", ".hidden", "Bad", "a b"} {
		if _, _, err := resolveWorkspace([]string{bad}); err == nil {
			t.Errorf("resolveWorkspace(%q) accepted, want rejection", bad)
		}
	}
}
