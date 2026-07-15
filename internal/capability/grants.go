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
// pure decision logic — no I/O. Consulted/updated by Grants, keyed by grant-set id.
type GrantStore interface {
	Allows(id string, call Call) bool
	Record(id string, call Call) error
}

// Grants is a permission set: the user's standing "allow" decisions for a session
// (today) or a workspace (later). It holds session-scoped grants (bound to its
// epoch — closing the epoch revokes them) and, via an injected store, always-
// scoped grants that persist. It may also carry a workspace-level Ceiling. The
// Guard consults the active Grants (carried in ctx) to see whether an Ask is
// already answered by a standing grant. A later workspace layer supplies a
// different id + a persistent store — no other change.
type Grants struct {
	ID      string
	Epoch   EpochID    // session grants bind here; closing it revokes them
	Ceiling *Ceiling   // optional workspace-level upper bound (nil = none)
	always  GrantStore // durable always-grants keyed by ID; nil = none

	mu      sync.Mutex
	session []Rule
}

// NewGrants builds a grant set. always may be nil (no durable grants — e.g. tests).
func NewGrants(id string, epoch EpochID, always GrantStore) *Grants {
	return &Grants{ID: id, Epoch: epoch, always: always}
}

// Allows reports whether call is covered by a live standing grant — a session
// grant (bound to a still-alive epoch, via env) or a persisted always grant.
func (g *Grants) Allows(call Call, env Env) bool {
	g.mu.Lock()
	sess := Policy{Rules: append([]Rule(nil), g.session...)}
	g.mu.Unlock()
	if sess.Evaluate(call, env) == Allow {
		return true
	}
	return g.always != nil && g.always.Allows(g.ID, call)
}

// Record stores a user's grant at scope. Once records nothing (the HITL outcome
// alone allows the one call). Session appends an epoch-bound Allow rule. Always
// persists through the durable store (a no-op if none is wired).
func (g *Grants) Record(call Call, scope Scope) error {
	switch scope {
	case ScopeSession:
		g.mu.Lock()
		g.session = append(g.session, Rule{
			Capability: call.Capability,
			TargetGlob: call.Target, // exact target; "" matches targetless calls
			Effect:     Allow,
			Epoch:      g.Epoch,
		})
		g.mu.Unlock()
	case ScopeAlways:
		if g.always != nil {
			return g.always.Record(g.ID, call)
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
