package gateway

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// Scope is a revocable permission scope: a live epoch on the Guard's registry plus
// the standing capability.Grants bound to it. It is the unit an interactive session
// or a single agent run holds and later revokes — the Guard owns the epoch mechanism
// so callers never touch the EpochRegistry directly.
//
// A Scope can only be minted by Guard.NewScope, so its epoch is ALWAYS the guard's
// own registry — the epoch the Guard checks for liveness during Authorize is exactly
// the one Revoke closes. That replaces the former comment-enforced invariant ("the
// registry passed here must be the guard's, so grants and revocation line up") with
// one the type system guarantees: there is no way to construct a Scope against the
// wrong registry.
type Scope struct {
	g      *Guard
	grants *capability.Grants
}

// NewScope opens a fresh epoch on the guard's registry and a standing grant set over
// store (nil = no durable "always" backing — session grants only). Session-scoped
// grants recorded under this scope bind to its epoch and die when Revoke closes it;
// "always" grants persist through store.
func (g *Guard) NewScope(store capability.GrantStore) *Scope {
	return &Scope{
		g:      g,
		grants: capability.NewGrants(g.epochRegistry().Open(), store),
	}
}

// Bind stamps this scope's grants (and its workspace cage, if any) onto ctx for one
// request, so the Guard binds standing grants to this scope and enforces the cage.
// Callers use this instead of reaching for capability.WithGrants/WithCage directly.
func (s *Scope) Bind(ctx context.Context) context.Context {
	ctx = capability.WithGrants(ctx, s.grants)
	if s.grants.Cage != nil {
		ctx = capability.WithCage(ctx, *s.grants.Cage)
	}
	return ctx
}

// Revoke closes the scope's epoch: every "Allow this session" grant bound to it stops
// matching at once (a stale replay is denied before any effect), while "always" grants
// in the durable store survive. Revoking an already-revoked scope is a no-op.
func (s *Scope) Revoke() { s.g.epochRegistry().Close(s.grants.Epoch) }
