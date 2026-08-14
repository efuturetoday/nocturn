package workspace

import (
	"encoding/json"
	"maps"
	"os"
	"sync"

	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// grantStore is a file-backed gate.Grants: RecallAlways approvals persist to a JSON file and survive
// a restart; session approvals live only in the in-memory gate.MemGrants it wraps.
type grantStore struct {
	path string
	mem  *gate.MemGrants

	mu      sync.Mutex
	durable map[gate.Grant]struct{} // the persisted set, guarded by mu
}

// newGrantStore opens (or creates) a grant store at path, loading any durable grants as the seed.
func newGrantStore(path string) (*grantStore, error) {
	durable, err := readGrants(path)
	if err != nil {
		return nil, err
	}
	seed := make([]gate.Grant, 0, len(durable))
	for g := range durable {
		seed = append(seed, g)
	}
	return &grantStore{path: path, mem: gate.NewMemGrants(seed...), durable: durable}, nil
}

// Allowed reports whether a standing grant covers the action, using the tool-supplied matcher.
func (s *grantStore) Allowed(a gate.Action, match gate.Matcher) bool { return s.mem.Allowed(a, match) }

// Remember records a grant. Every grant enters the in-memory set; a RecallAlways grant is also
// written to disk so it survives a restart. (gate.Check never calls with RecallNever.)
func (s *grantStore) Remember(g gate.Grant, recall gate.Recall) {
	s.mem.Remember(g, recall)
	if recall != gate.RecallAlways {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.durable[g]; ok {
		return
	}
	s.durable[g] = struct{}{}
	// Best-effort: a failed write drops durability to session-only, never blocks the action.
	_ = s.write()
}

// Forget drops a grant from both halves, reporting whether it was there. The durable set is rewritten
// so it does not come back at the next start — a revocation that only held until a restart would be
// the worst kind, since nobody would be watching when it lapsed.
func (s *grantStore) Forget(g gate.Grant) bool {
	had := s.mem.Forget(g)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.durable[g]; !ok {
		return had
	}
	delete(s.durable, g)
	// Best-effort, like Remember: a failed write leaves the grant revoked in memory and back after a
	// restart. Logging it is the caller's, which is why Forget reports what it did.
	_ = s.write()
	return true
}

// Standing is one remembered approval as a consumer shows it: the grant, plus whether it survives a
// restart. The distinction is the whole of what a person needs to judge one — "until this daemon
// stops" and "forever" are different answers to the same question, and only the second accumulates.
type Standing struct {
	Grant   gate.Grant
	Durable bool
}

// List returns every standing grant, durable ones marked.
//
// The in-memory set is the truth about what will be allowed right now — it holds the durable ones
// too, seeded at open — so it is the source, and the file only says which of them outlive a restart.
func (s *grantStore) List() []Standing {
	s.mu.Lock()
	durable := make(map[gate.Grant]struct{}, len(s.durable))
	maps.Copy(durable, s.durable)
	s.mu.Unlock()

	all := s.mem.All()
	out := make([]Standing, 0, len(all))
	for _, g := range all {
		_, isDurable := durable[g]
		out = append(out, Standing{Grant: g, Durable: isDurable})
	}
	return out
}

// write persists the durable set atomically (write then rename). Callers hold s.mu.
func (s *grantStore) write() error {
	list := make([]gate.Grant, 0, len(s.durable))
	for g := range s.durable {
		list = append(list, g)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// readGrants reads the durable grant set from path, treating a missing file as empty.
func readGrants(path string) (map[gate.Grant]struct{}, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[gate.Grant]struct{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	var list []gate.Grant
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	set := make(map[gate.Grant]struct{}, len(list))
	for _, g := range list {
		set[g] = struct{}{}
	}
	return set, nil
}

var _ gate.Grants = (*grantStore)(nil)
