// Package persona owns one workspace's persona — the assistant's system prompt. It is a
// self-contained STATE service: it resolves the layered PERSONA.md (workspace override →
// shared default → built-in), holds the current value, and persists a change — with its
// OWN synchronization. Callers deal in the resolved string and never see the file or a
// lock.
//
// This is the pattern for runtime-mutable state: the state and its synchronization live in
// the small service that OWNS it, so adding a mutable-config feature means adding a service
// like this — never a mutex per field on a larger aggregate. The Workspace holds a *Store
// and just delegates; it grows no locks.
//
// PERSONA.md lives in the workspace ROOT — control-plane, never under mnt/ (ADR-10) — so
// the model can neither read nor rewrite its own identity; a self-writable persona would be
// a prompt-injection vector onto the assistant itself.
package persona

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Default is the built-in persona used when no PERSONA.md exists at any layer.
const Default = "You are Nocturn, a careful assistant. " +
	"Use a tool when it helps; otherwise answer directly."

// Store owns one workspace's persona. dir is the workspace directory — its PERSONA.md is
// the writable layer; the shared PERSONA.md one level up is a read-only fallback.
type Store struct {
	dir string

	mu      sync.RWMutex
	current string // the resolved persona
}

// Load resolves the persona for the workspace at dir and returns a Store holding it.
func Load(dir string) *Store {
	return &Store{dir: dir, current: resolve(dir)}
}

// resolve applies OVERRIDE semantics (first non-blank wins): the workspace's own
// PERSONA.md, else the shared PERSONA.md in the parent directory, else Default.
func resolve(dir string) string {
	for _, p := range []string{
		filepath.Join(dir, "PERSONA.md"),
		filepath.Join(filepath.Dir(dir), "PERSONA.md"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s
			}
		}
	}
	return Default
}

// Get returns the current resolved persona.
func (s *Store) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Set persists text to the workspace's PERSONA.md and updates the in-memory value. It then
// RE-RESOLVES, so writing a blank persona correctly falls back to the shared/default layer
// rather than pinning an empty prompt. The next session (OpenSession / Reset) picks it up.
func (s *Store) Set(text string) error {
	if err := os.WriteFile(filepath.Join(s.dir, "PERSONA.md"), []byte(text), 0o644); err != nil {
		return err
	}
	resolved := resolve(s.dir)
	s.mu.Lock()
	s.current = resolved
	s.mu.Unlock()
	return nil
}
