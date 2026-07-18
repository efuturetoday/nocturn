package grantstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// Store is the durable "always" backing for ONE owner's capability.Grants set
// (implements capability.GrantStore): the grants a user chose to remember across
// restarts. The backing FILE is the owner boundary — a session's is <ws>/grants.json,
// an agent's is <ws>/agents/<name>/grants.json — so records carry no owner id and two
// owners can never cross-match (isolation is structural). File-backed (0600),
// concurrency-safe. A missing or unparsable file yields an empty store — fail-closed,
// so corrupt persisted grants simply don't apply rather than widening authority.
type Store struct {
	path string
	mu   sync.Mutex
	recs []grantRecord
}

type grantRecord struct {
	Tool   string `json:"tool"`
	Family string `json:"family"`
	Write  bool   `json:"write"`
	Target string `json:"target"`
}

var _ capability.GrantStore = (*Store)(nil)

// grantsFile is the per-owner grants filename (a session's is <ws>/grants.json, an
// agent's is <ws>/agents/<name>/grants.json).
const grantsFile = "grants.json"

// Path returns the per-agent grants file inside its folder. The folder is the
// portable/purgeable owner unit (ADR-10): deleting it removes the agent AND its
// grants, and one owner's grants can never cross-match another's.
func Path(agentsDir, name string) string {
	return filepath.Join(agentsDir, name, grantsFile)
}

// Load reads path (missing/invalid → empty store, fail-closed).
func Load(path string) *Store {
	s := &Store{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s.recs)
	}
	return s
}

// Allows reports whether an "always" grant exactly matches (tool, family, mutation,
// target) — grants are recorded from a live call, so the target is exact. The tool is
// part of the key: an "always gmail.send" record never matches a call the model made
// through "gmail.delete", even to the same host. Records written by an older format
// (with a grant_set / capability field) decode with the new fields zero and so no
// longer match any real call — fail-closed, the user simply re-approves once.
func (s *Store) Allows(tool string, call capability.Call) bool {
	rec := recordFor(tool, call)
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.recs, rec)
}

// Record persists an "always" grant (idempotent), writing the file atomically.
func (s *Store) Record(tool string, call capability.Call) error {
	rec := recordFor(tool, call)
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.Contains(s.recs, rec) {
		return nil
	}
	s.recs = append(s.recs, rec)
	return s.persist()
}

// View is one persisted "always" grant, for listing/revoking in the UI.
type View struct {
	Tool   string
	Family string
	Write  bool
	Target string
}

// List returns the persisted "always" grants (a snapshot), for a /grants listing.
func (s *Store) List() []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]View, len(s.recs))
	for i, r := range s.recs {
		out[i] = View(r)
	}
	return out
}

// Remove deletes one persisted grant (matched exactly) and rewrites the file. A
// no-op if not present. Used to revoke an "always" grant from the UI.
func (s *Store) Remove(g View) error {
	target := grantRecord(g)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.recs {
		if r == target {
			s.recs = append(s.recs[:i], s.recs[i+1:]...)
			return s.persist()
		}
	}
	return nil
}

func recordFor(tool string, call capability.Call) grantRecord {
	return grantRecord{Tool: tool, Family: call.Family, Write: call.Write, Target: call.Target}
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
