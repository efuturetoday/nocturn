package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// GrantsStore is the durable "always" permission set (implements
// capability.PersistentGrants): the grants a user chose to remember across
// restarts, keyed by context/workspace id. File-backed (0600), concurrency-safe.
// A missing or unparsable file yields an empty store — fail-closed, so corrupt
// persisted grants simply don't apply rather than crashing or widening authority.
type GrantsStore struct {
	path string
	mu   sync.Mutex
	recs []grantRecord
}

type grantRecord struct {
	Context    string `json:"context"`
	Capability string `json:"capability"`
	Host       string `json:"host"`
}

var _ capability.PersistentGrants = (*GrantsStore)(nil)

// LoadGrantsStore reads path (missing/invalid → empty store, fail-closed).
func LoadGrantsStore(path string) *GrantsStore {
	s := &GrantsStore{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s.recs)
	}
	return s
}

// Allows reports whether an "always" grant exactly matches (context, capability,
// host) — grants are recorded from a live call, so the host is exact.
func (s *GrantsStore) Allows(contextID string, call capability.Call) bool {
	rec := recordFor(contextID, call)
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
func (s *GrantsStore) Record(contextID string, call capability.Call) error {
	rec := recordFor(contextID, call)
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

func recordFor(contextID string, call capability.Call) grantRecord {
	return grantRecord{Context: contextID, Capability: call.Capability, Host: call.Attrs["host"]}
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
