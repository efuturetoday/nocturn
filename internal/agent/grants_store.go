package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// GrantsStore is the durable "always" backing for ONE owner's capability.Grants set
// (implements capability.GrantStore): the grants a user chose to remember across
// restarts. The backing FILE is the owner boundary — a session's is <ws>/grants.json,
// an agent's is <ws>/agents/<name>/grants.json — so records carry no owner id and two
// owners can never cross-match (isolation is structural). File-backed (0600),
// concurrency-safe. A missing or unparsable file yields an empty store — fail-closed,
// so corrupt persisted grants simply don't apply rather than widening authority.
type GrantsStore struct {
	path string
	mu   sync.Mutex
	recs []grantRecord
}

type grantRecord struct {
	Tool    string `json:"tool"`
	Family  string `json:"family"`
	Mutates bool   `json:"mutates"`
	Target  string `json:"target"`
}

var _ capability.GrantStore = (*GrantsStore)(nil)

// LoadGrantsStore reads path (missing/invalid → empty store, fail-closed).
func LoadGrantsStore(path string) *GrantsStore {
	s := &GrantsStore{path: path}
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
func (s *GrantsStore) Allows(tool string, call capability.Call) bool {
	rec := recordFor(tool, call)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r == rec {
			return true
		}
	}
	return false
}

// Record persists an "always" grant (idempotent), writing the file atomically.
func (s *GrantsStore) Record(tool string, call capability.Call) error {
	rec := recordFor(tool, call)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r == rec {
			return nil
		}
	}
	s.recs = append(s.recs, rec)
	return s.persist()
}

// GrantView is one persisted "always" grant, for listing/revoking in the UI.
type GrantView struct {
	Tool    string
	Family  string
	Mutates bool
	Target  string
}

// List returns the persisted "always" grants (a snapshot), for a /grants listing.
func (s *GrantsStore) List() []GrantView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]GrantView, len(s.recs))
	for i, r := range s.recs {
		out[i] = GrantView(r)
	}
	return out
}

// Remove deletes one persisted grant (matched exactly) and rewrites the file. A
// no-op if not present. Used to revoke an "always" grant from the UI.
func (s *GrantsStore) Remove(g GrantView) error {
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
	return grantRecord{Tool: tool, Family: call.Family, Mutates: call.Mutates, Target: call.Target}
}

func (s *GrantsStore) persist() error {
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
