package gate

import (
	"slices"
	"strings"
	"sync"
)

// Grant is a remembered approval — a (Kind, Target) pattern a human has allowed. What a Target
// pattern MEANS is decided by a Matcher supplied at Check time, not by this library; "*" for any is
// the only structure gate itself knows.
type Grant struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// Matcher reports whether a grant's target PATTERN covers an action's Target. This is where target
// semantics live — host suffixes, path globs — which gate deliberately does not know: the caller
// that owns those semantics (the tool) passes it to Check. Kind matching is structural and done by
// gate; a Matcher concerns only the Target. Nil = ExactMatch.
type Matcher func(pattern, target string) bool

// ExactMatch is the opaque default: "*" matches anything, otherwise equality. No host/path semantics.
func ExactMatch(pattern, target string) bool { return pattern == "*" || pattern == target }

func matchKind(pattern, kind string) bool { return pattern == "*" || pattern == kind }

// Grants stores remembered approvals. Allowed reports whether an action is covered by any grant,
// using the supplied Matcher for the Target (nil = ExactMatch); Remember records a new grant at the
// given Recall (RecallSession = keep for this session, RecallAlways = keep durably; Check never calls
// Remember with RecallNever); Forget drops one, reporting whether it was there.
//
// Forget exists because a remembered approval outlives the thing it was granted for. An answer to
// "may this reach api.example.com?" is stored as (Kind, Target) and records nothing about why — so
// when the consumer removes whatever prompted the question, only the consumer can know the grant is
// now standing on its own. Without a way to drop it, the next thing to reach that target inherits an
// answer nobody gave for it.
//
// A durable implementation is the consumer's; MemGrants is the in-memory default.
type Grants interface {
	Allowed(a Action, match Matcher) bool
	Remember(g Grant, recall Recall)
	Forget(g Grant) bool
}

// MemGrants is an in-memory Grants. It has no durable backing, so Always is remembered only for the
// life of the process. The grants are a set, so repeated approvals don't accumulate. Safe for
// concurrent use.
type MemGrants struct {
	mu  sync.Mutex
	set map[Grant]struct{}
}

// NewMemGrants builds an in-memory grant set, optionally seeded with standing grants.
func NewMemGrants(seed ...Grant) *MemGrants {
	m := &MemGrants{set: make(map[Grant]struct{}, len(seed))}
	for _, g := range seed {
		m.set[g] = struct{}{}
	}
	return m
}

func (m *MemGrants) Allowed(a Action, match Matcher) bool {
	if match == nil {
		match = ExactMatch
	}
	// Snapshot under the lock, then run the caller-supplied Matcher outside it — never hold the lock
	// across an arbitrary callback.
	m.mu.Lock()
	grants := make([]Grant, 0, len(m.set))
	for g := range m.set {
		grants = append(grants, g)
	}
	m.mu.Unlock()

	for _, g := range grants {
		if matchKind(g.Kind, a.Kind) && match(g.Target, a.Target) {
			return true
		}
	}
	return false
}

func (m *MemGrants) Remember(g Grant, _ Recall) {
	// In-memory: everything lasts the process; the Recall (session vs durable) matters only to a
	// durable store.
	m.mu.Lock()
	m.set[g] = struct{}{}
	m.mu.Unlock()
}

// All returns the standing grants, sorted, so a consumer can SHOW them.
//
// A remembered approval is authority that outlives the moment it was given, and one nobody can see is
// one nobody revokes. Forget already exists for the consumer that knows a grant has become stale;
// this is for the consumer that wants to ask a human, which cannot be done without a list.
func (m *MemGrants) All() []Grant {
	m.mu.Lock()
	out := make([]Grant, 0, len(m.set))
	for g := range m.set {
		out = append(out, g)
	}
	m.mu.Unlock()

	slices.SortFunc(out, func(a, b Grant) int {
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return strings.Compare(a.Target, b.Target)
	})
	return out
}

// Forget drops a grant, reporting whether it was there.
func (m *MemGrants) Forget(g Grant) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, had := m.set[g]
	delete(m.set, g)
	return had
}

var _ Grants = (*MemGrants)(nil)
