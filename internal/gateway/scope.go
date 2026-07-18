package gateway

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// Authority is the complete permission envelope a Scope binds for every turn: the
// standing grants plus the author restrictions (policy tightening, cage bound, autonomy
// dial) and a provenance label. It is the ONE construction input that decides what a
// chat may do — a workspace (root) chat's authority is just its grants (unrestricted);
// an agent chat's authority carries the agent's declared restrictions. Every field's
// zero value is inert, so Authority{Grants: store} reproduces a plain session scope.
type Authority struct {
	Grants   capability.GrantStore // durable "always" backing; nil = session grants only
	Policy   capability.Policy     // author tightening, unioned onto the base (deny>ask>allow); no rules = none
	Cage     []capability.Pair     // reachability upper bound; empty = no extra bound
	Autonomy capability.Autonomy   // how an unattended Ask resolves; AutonomyAttended (zero) = a human is present
	Label    string                // provenance prefix on out-of-band prompts, e.g. "work/mail-triage"
}

// Scope is a revocable permission scope: a live epoch on the Guard's registry plus the
// standing capability.Grants bound to it, and the Authority it enforces on every Bind.
// It is the unit a chat holds and later revokes — the Guard owns the epoch mechanism so
// callers never touch the EpochRegistry directly.
//
// A Scope can only be minted by Guard.NewScope, so its epoch is ALWAYS the guard's own
// registry — the epoch the Guard checks for liveness during Authorize is exactly the one
// Revoke closes; there is no way to construct a Scope against the wrong registry.
type Scope struct {
	g      *Guard
	grants *capability.Grants
	auth   Authority
}

// NewScope opens a fresh epoch on the guard's registry and a standing grant set over the
// authority's store (nil = session grants only). Session-scoped grants recorded under this
// scope bind to its epoch and die when Revoke closes it; "always" grants persist.
func (g *Guard) NewScope(a Authority) *Scope {
	return &Scope{
		g:      g,
		grants: capability.NewGrants(g.epochRegistry().Open(), a.Grants),
		auth:   a,
	}
}

// Bind stamps this scope's whole authority onto ctx for one request — grants, cage(s),
// policy tightening, autonomy dial, and label — in one call. Callers use this instead of
// reaching for the capability.With* helpers directly, so a chat's permissions enter the
// ctx in exactly one place. The scope is the single authority: autonomy is ALWAYS stamped
// (the attended default included), so a turn's dial is whatever THIS scope declares, never
// an ambient value leaking in. Empty policy/cage/label are no-ops.
func (s *Scope) Bind(ctx context.Context) context.Context {
	ctx = capability.WithGrants(ctx, s.grants)
	if s.grants.Cage != nil {
		ctx = capability.WithCage(ctx, *s.grants.Cage)
	}
	if len(s.auth.Cage) > 0 {
		ctx = capability.WithCage(ctx, capability.NewCage(s.auth.Cage...))
	}
	if len(s.auth.Policy.Rules) > 0 {
		ctx = capability.WithPolicy(ctx, s.auth.Policy)
	}
	ctx = capability.WithAutonomy(ctx, s.auth.Autonomy)
	if s.auth.Label != "" {
		ctx = WithLabel(ctx, s.auth.Label)
	}
	return ctx
}

// Revoke closes the scope's epoch: every "Allow this session" grant bound to it stops
// matching at once (a stale replay is denied before any effect), while "always" grants
// in the durable store survive. Revoking an already-revoked scope is a no-op.
func (s *Scope) Revoke() { s.g.epochRegistry().Close(s.grants.Epoch) }
