package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverWorkspaces(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"default", "work", "a-b_c"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A non-workspace file (the shared master descriptor) and an unsafe-named dir must
	// be skipped — only valid workspace directories are returned, sorted.
	if err := os.WriteFile(filepath.Join(root, "master.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Bad Name"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := discoverWorkspaces(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a-b_c", "default", "work"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discoverWorkspaces = %v, want %v", got, want)
	}

	// A missing root is not an error (no workspaces yet).
	if ws, err := discoverWorkspaces(filepath.Join(root, "absent")); err != nil || ws != nil {
		t.Fatalf("missing root: ws=%v err=%v, want nil,nil", ws, err)
	}
}
