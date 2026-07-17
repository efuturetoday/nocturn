// Package approval is the durable "already reviewed" record for plugins and MCP
// servers: the last-approved declaration of each, so an UNCHANGED one needs no
// re-prompt on every boot while a CHANGED one is re-surfaced (as the
// security-relevant event) with a diff. It is a REVIEW memo, NOT authority —
// every effect an installed plugin or connected server performs is still gated
// by the broker + HITL + cage. It deliberately does NOT touch capability
// grants (grants.json), which are the standing effect-authority.
//
// File-backed (0600), concurrency-safe, stdlib-only, mirroring
// grantstore.Store. A missing or unparsable file yields an EMPTY store —
// fail-safe: nothing counts as approved, so everything re-prompts rather than
// silently auto-approving on a corrupt record. The file lives in the workspace
// control-plane (outside the model's mount, ADR-10), so the model cannot
// self-approve a plugin it wrote.
package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Store is the set of last-approved declarations, keyed by "<kind>:<name>".
type Store struct {
	path string
	mu   sync.Mutex
	recs map[string]record
}

// record keeps a hash (for the unchanged check — robust across re-serialization)
// and the declaration itself (for showing a diff when it changed).
type record struct {
	Hash    string          `json:"hash"`
	Content json.RawMessage `json:"content"`
}

// Load reads path; a missing or unparsable file yields an empty store (fail-safe,
// so a corrupt record never silently auto-approves).
func Load(path string) *Store {
	s := &Store{path: path, recs: map[string]record{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s.recs)
		if s.recs == nil {
			s.recs = map[string]record{}
		}
	}
	return s
}

// Status reports whether (kind, name) was last approved with exactly this content
// and returns the previously-approved declaration (nil if none) so the caller can
// show what changed.
func (s *Store) Status(kind, name string, content []byte) (approved bool, prior []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.recs[key(kind, name)]
	if !ok {
		return false, nil
	}
	return rec.Hash == hashOf(content), append([]byte(nil), rec.Content...)
}

// Approve records content as the approved declaration for (kind, name) and
// persists the store atomically.
func (s *Store) Approve(kind, name string, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[key(kind, name)] = record{Hash: hashOf(content), Content: append(json.RawMessage(nil), content...)}
	return s.persist()
}

func key(kind, name string) string { return kind + ":" + name }

func hashOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (s *Store) persist() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.recs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
