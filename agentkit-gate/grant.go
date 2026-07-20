package gate

import "sync"

// Grant is a remembered approval — an (Tool, Target) pair a human has allowed. "*" is a wildcard:
// Target "*" allows any target for that tool; Tool "*" allows any tool for that target.
type Grant struct {
	Tool   string
	Target string
}

// Grants stores remembered approvals. Allowed reports whether an action is already covered by a
// grant; Remember records a new one at the given scope (Once = session, Always = durable). A durable
// implementation is the consumer's; MemGrants is the in-memory, session-only default.
type Grants interface {
	Allowed(a Action) bool
	Remember(a Action, s Scope)
}

// MemGrants is an in-memory Grants. It has no durable backing, so Always is remembered only for the
// life of the process (same as Once). Safe for concurrent use.
type MemGrants struct {
	mu  sync.Mutex
	set map[Grant]bool
}

// NewMemGrants builds an empty in-memory grant set, optionally seeded with standing grants.
func NewMemGrants(seed ...Grant) *MemGrants {
	m := &MemGrants{set: make(map[Grant]bool, len(seed))}
	for _, g := range seed {
		m.set[g] = true
	}
	return m
}

func (m *MemGrants) Allowed(a Action) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range covering(a) {
		if m.set[g] {
			return true
		}
	}
	return false
}

func (m *MemGrants) Remember(a Action, _ Scope) {
	// No durable backing: Once and Always are the same in memory.
	m.mu.Lock()
	m.set[Grant{Tool: a.Tool, Target: a.Target}] = true
	m.mu.Unlock()
}

var _ Grants = (*MemGrants)(nil)

// covering lists the grants that would permit action a: the exact pair plus the wildcard forms.
func covering(a Action) [4]Grant {
	return [4]Grant{
		{a.Tool, a.Target},
		{a.Tool, "*"},
		{"*", a.Target},
		{"*", "*"},
	}
}
