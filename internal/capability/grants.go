package capability

import (
	"context"
	"sync"
)

// Scope is how long a user-granted "allow" lasts.
type Scope int

const (
	// ScopeOnce allows a single call and records nothing — the next matching call asks again.
	ScopeOnce Scope = iota
	// ScopeSession allows until the grants' epoch closes (revoked on Reset/Close).
	ScopeSession
	// ScopeAlways persists across restarts (stored in the durable grant set).
	ScopeAlways
)

// GrantStore is the durable "always" backing for a Grants set. It is an interface
// so the concrete store (a file) lives in an outer layer and this package stays
// pure decision logic — no I/O. ONE store = ONE owner (a session, or one agent): the
// backing FILE is the owner boundary, so records carry no owner id — strict
// isolation is structural (a different owner is a different file), not a shared file
// keyed by a string (see KONZEPT §9). A grant is keyed by the model-facing TOOL and
// the (family, mutation, target): "always gmail.send" never covers "gmail.delete".
type GrantStore interface {
	Allows(tool string, call Call) bool
	Record(tool string, call Call) error
}

// Grants is a permission set: the user's standing "allow" decisions for a session
// (today) or a workspace (later). It holds session-scoped grants (bound to its
// epoch — closing the epoch revokes them) and, via an injected store, always-
// scoped grants that persist. It may also carry a workspace-level Ceiling. The
// Guard consults the active Grants (carried in ctx) to see whether an Ask is
// already answered by a standing grant. Each owner (session / one agent) has its OWN
// Grants over its OWN store — strict isolation, no cross-owner sharing.
type Grants struct {
	Epoch   EpochID    // session grants bind here; closing it revokes them
	Ceiling *Ceiling   // optional workspace-level upper bound (nil = none)
	always  GrantStore // durable always-grants for this owner; nil = none

	mu      sync.Mutex
	session []sessionGrant
}

// sessionGrant is one session-scoped standing grant: the model-facing tool it was
// recorded for, plus the epoch-bound match rule. A call is covered only if BOTH
// the tool matches and the rule matches — same tool-scoping as the durable store.
type sessionGrant struct {
	tool string
	rule Rule
}

// writeMatch maps a call's mutation flag to the Match a recorded grant should use,
// so a grant covers exactly the read/write class it was approved for.
func writeMatch(mutates bool) Match {
	if mutates {
		return MatchWrite
	}
	return MatchRead
}

// NewGrants builds a grant set for one owner over its store (nil = no durable
// grants — e.g. tests), bound to epoch for its session-scoped grants.
func NewGrants(epoch EpochID, always GrantStore) *Grants {
	return &Grants{Epoch: epoch, always: always}
}

// Allows reports whether call, made through tool, is covered by a live standing
// grant — a session grant (bound to a still-alive epoch, via env) or a persisted
// always grant. Both are tool-scoped: a grant recorded for one tool never covers a
// call the model made through a different tool, even to the same target.
func (g *Grants) Allows(tool string, call Call, env Env) bool {
	g.mu.Lock()
	sess := append([]sessionGrant(nil), g.session...)
	g.mu.Unlock()
	for _, sg := range sess {
		if sg.tool == tool && sg.rule.matches(call, env) {
			return true
		}
	}
	return g.always != nil && g.always.Allows(tool, call)
}

// Record stores a user's grant at scope, remembered against tool (the model-facing
// tool the human approved). Once records nothing (the HITL outcome alone allows the
// one call). Session appends an epoch-bound Allow rule. Always persists through the
// durable store (a no-op if none is wired).
func (g *Grants) Record(tool string, call Call, scope Scope) error {
	switch scope {
	case ScopeSession:
		g.mu.Lock()
		g.session = append(g.session, sessionGrant{tool: tool, rule: Rule{
			Family:     call.Family,
			TargetGlob: call.Target, // exact target; "" matches targetless calls
			Writes:     writeMatch(call.Mutates),
			Effect:     Allow,
			Epoch:      g.Epoch,
		}})
		g.mu.Unlock()
	case ScopeAlways:
		if g.always != nil {
			return g.always.Record(tool, call)
		}
	}
	return nil
}

// The active Grants set travels through the request context so the Guard reads it
// without widening every signature — same idiom as the epoch.

type grantsKey struct{}

// WithGrants returns a ctx carrying g as the active permission set.
func WithGrants(ctx context.Context, g *Grants) context.Context {
	return context.WithValue(ctx, grantsKey{}, g)
}

// GrantsFrom returns the active Grants set, or nil if none (a caller with no
// standing-grant home — every effect then asks).
func GrantsFrom(ctx context.Context) *Grants {
	g, _ := ctx.Value(grantsKey{}).(*Grants)
	return g
}
