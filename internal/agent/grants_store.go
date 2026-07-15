package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// GrantsStore is the durable "always" backing for a capability.Grants set
// (implements capability.GrantStore): the grants a user chose to remember across
// restarts, keyed by grant-set/workspace id. File-backed (0600), concurrency-safe.
// A missing or unparsable file yields an empty store — fail-closed, so corrupt
// persisted grants simply don't apply rather than crashing or widening authority.
type GrantsStore struct {
	path string
	mu   sync.Mutex
	recs []grantRecord
}

type grantRecord struct {
	GrantSet   string `json:"grant_set"`
	Capability string `json:"capability"`
	Target     string `json:"target"`
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

// Allows reports whether an "always" grant exactly matches (grant-set, capability,
// host) — grants are recorded from a live call, so the host is exact.
func (s *GrantsStore) Allows(grantSetID string, call capability.Call) bool {
	rec := recordFor(grantSetID, call)
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
func (s *GrantsStore) Record(grantSetID string, call capability.Call) error {
	rec := recordFor(grantSetID, call)
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

func recordFor(grantSetID string, call capability.Call) grantRecord {
	return grantRecord{GrantSet: grantSetID, Capability: call.Capability, Target: call.Target}
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
