package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// wsFixture makes <cwd>/nocturn-data/workspaces/<name> the working tree, so the wsRoot constant
// resolves into a temp dir. t.Chdir restores the old directory and forbids parallel tests, which is
// what we want: wsRoot is a constant and cannot be redirected any other way.
func wsFixture(t *testing.T, name string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, wsRoot, name), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
}

func TestOpenVoices(t *testing.T) {
	// Each subtest gets its own tree. Sharing one would make the empty-set case depend on running
	// before the enrolment case, and a later reordering would break it for no visible reason.
	t.Run("missing file is an empty set", func(t *testing.T) {
		wsFixture(t, "main")
		profiles, err := openVoices("main")
		if err != nil {
			t.Fatalf("openVoices: %v", err)
		}
		if got := profiles.Names(); len(got) != 0 {
			t.Errorf("names = %v, want none", got)
		}
	})

	t.Run("enrolment survives a reopen", func(t *testing.T) {
		wsFixture(t, "main")
		profiles, err := openVoices("main")
		if err != nil {
			t.Fatalf("openVoices: %v", err)
		}
		if err := profiles.Enrol("lina", "satellite", []float32{1, 0, 0}); err != nil {
			t.Fatalf("enrol: %v", err)
		}
		reopened, err := openVoices("main")
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if got := reopened.Names(); !slices.Equal(got, []string{"lina"}) {
			t.Errorf("names after reopen = %v, want [lina]", got)
		}
	})

	t.Run("empty name means the default workspace", func(t *testing.T) {
		wsFixture(t, "main")
		if _, err := openVoices(""); err != nil {
			t.Errorf("openVoices(\"\") = %v, want the main workspace", err)
		}
	})

	// A typo'd -w must say so rather than fail later inside save() with a bare ENOENT.
	t.Run("unknown workspace names itself", func(t *testing.T) {
		wsFixture(t, "main")
		_, err := openVoices("typo")
		if err == nil {
			t.Fatal("openVoices(typo) = nil error, want one")
		}
		if got := err.Error(); !strings.Contains(got, "typo") {
			t.Errorf("error = %q, want it to name the workspace", got)
		}
	})
}

func TestWavsIn(t *testing.T) {
	dir := t.TempDir()
	// Deliberately created out of order, so a passing order assertion means sorting and not luck.
	for _, name := range []string{"b.wav", "a.WAV", "notes.txt", "c.wav"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested.wav"), 0o700); err != nil {
		t.Fatal(err) // a directory named like a recording must not be read as one
	}
	loose := filepath.Join(t.TempDir(), "loose.aiff")
	if err := os.WriteFile(loose, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("a directory contributes its wavs, sorted", func(t *testing.T) {
		got, err := wavsIn([]string{dir})
		if err != nil {
			t.Fatalf("wavsIn: %v", err)
		}
		want := []string{
			filepath.Join(dir, "a.WAV"), // extension match is case-insensitive
			filepath.Join(dir, "b.wav"),
			filepath.Join(dir, "c.wav"),
		}
		if !slices.Equal(got, want) {
			t.Errorf("wavsIn = %v, want %v", got, want)
		}
	})

	// A named file is taken at its word — the operator asked for it, and ReadWAV is strict enough
	// to reject it later if it is not really a recording.
	t.Run("a named file is taken whatever its extension", func(t *testing.T) {
		got, err := wavsIn([]string{loose})
		if err != nil {
			t.Fatalf("wavsIn: %v", err)
		}
		if !slices.Equal(got, []string{loose}) {
			t.Errorf("wavsIn = %v, want [%s]", got, loose)
		}
	})

	t.Run("a missing path is an error, not an empty result", func(t *testing.T) {
		if _, err := wavsIn([]string{filepath.Join(dir, "nope")}); err == nil {
			t.Error("wavsIn(missing) = nil error, want one")
		}
	})
}
