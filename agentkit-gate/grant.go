package gate

import "sync"

// Grant is a remembered approval — a (Kind, Target) pattern a human has allowed. What a Target
// pattern MEANS is decided by a Matcher supplied at Check time, not by this library; "*" for any is
// the only structure gate itself knows.
type Grant struct {
	Kind   string
	Target string
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
// using the supplied Matcher for the Target (nil = ExactMatch); Remember records a new grant (Once =
// session, Always = durable). A durable implementation is the consumer's; MemGrants is the in-memory
// default.
type Grants interface {
	Allowed(a Action, match Matcher) bool
	Remember(g Grant, s Scope)
}

// MemGrants is an in-memory Grants. It has no durable backing, so Always is remembered only for the
// life of the process. Safe for concurrent use.
type MemGrants struct {
	mu     sync.Mutex
	grants []Grant
}

// NewMemGrants builds an in-memory grant set, optionally seeded with standing grants.
func NewMemGrants(seed ...Grant) *MemGrants {
	m := &MemGrants{grants: make([]Grant, 0, len(seed))}
	m.grants = append(m.grants, seed...)
	return m
}

func (m *MemGrants) Allowed(a Action, match Matcher) bool {
	if match == nil {
		match = ExactMatch
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.grants {
		if matchKind(g.Kind, a.Kind) && match(g.Target, a.Target) {
			return true
		}
	}
	return false
}

func (m *MemGrants) Remember(g Grant, _ Scope) {
	m.mu.Lock()
	m.grants = append(m.grants, g)
	m.mu.Unlock()
}

var _ Grants = (*MemGrants)(nil)
