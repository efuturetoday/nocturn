// Package gateway is the authorization use-case: the one reusable pipeline that
// decides whether a real-world effect may proceed.
//
// Its core is the Guard: the broker decision (allow / ask / deny, with epoch,
// time window, and rate limit) plus out-of-band human approval on Ask, wrapped by
// Do (authorize-then-execute). The concrete effects live OUTSIDE this package, in
// interface-adapter packages (e.g. netcap for http/dns) that each hold a *Guard
// and run every call through it before doing any I/O. Adding a capability is a new
// small adapter type — never a new field on a god-object here. This package
// therefore depends only inward (capability + hitl), not on any effect adapter.
package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// ErrDenied is returned when a capability call is not permitted (broker Deny, or
// a human denied / did not approve in time).
var ErrDenied = errors.New("gateway: capability denied")

// Guard authorizes capability calls. It is host-trusted and shared by every
// capability group. It is a pure COMPOSER: the standing-grant state lives on the
// active capability.Grants (not on the Guard), and upper bounds live in the
// ceiling chain carried by ctx — so the Guard holds no per-session mutable state.
type Guard struct {
	Policy    capability.Policy
	Approvals *hitl.Engine
	Epochs    *capability.EpochRegistry
	Rate      *capability.RateLimiter
	TTL       time.Duration
	Now       func() time.Time
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// approvalChoices are the options a human is offered on Ask.
var approvalChoices = []hitl.Choice{
	{Label: "Allow once", Outcome: hitl.Approved},
	{Label: "Allow this session", Outcome: hitl.ApprovedSession},
	{Label: "Allow always", Outcome: hitl.ApprovedAlways},
	{Label: "Deny", Outcome: hitl.Denied},
}

// Authorize composes the decision:
//  1. Ceiling chain (ctx): outside the intersection of all in-scope upper bounds
//     → hard deny, never even asking — so a prompt-injected caller can't get you
//     to approve something it was never allowed to attempt.
//  2. Base policy: Allow → proceed; Deny → deny (deny-wins hard rail).
//  3. On Ask: a standing grant in the active grant set (session or always) short-
//     circuits; otherwise out-of-band human approval, and the chosen scope
//     (once/session/always) is recorded as a grant on the grant set.
func (g *Guard) Authorize(ctx context.Context, call capability.Call, intent string) error {
	if !capability.WithinCeilings(ctx, call) {
		return ErrDenied
	}
	env := capability.Env{Now: g.now(), Epochs: g.Epochs}
	if g.Rate != nil {
		env.RateAllow = func(c capability.Call) bool { return g.Rate.Allow(c.Capability) }
	}

	switch g.Policy.Evaluate(call, env) {
	case capability.Allow:
		return nil
	case capability.Ask:
		grants := capability.GrantsFrom(ctx)
		if grants != nil && grants.Allows(call, env) {
			return nil // standing grant (session or always)
		}
		out, err := g.Approvals.Request(ctx, intent, approvalChoices, g.TTL)
		if err != nil {
			return err
		}
		switch out {
		case hitl.ApprovedAlways:
			if grants != nil {
				_ = grants.Record(call, capability.ScopeAlways) // persist error must not block the allow
			}
			return nil
		case hitl.ApprovedSession:
			if grants != nil {
				_ = grants.Record(call, capability.ScopeSession)
			}
			return nil
		case hitl.Approved:
			return nil
		default:
			return ErrDenied
		}
	default:
		return ErrDenied
	}
}

// Do authorizes call (out-of-band HITL on Ask) and runs effect ONLY if the call
// is allowed. Because the effect is a closure, it is unreachable unless Authorize
// returned nil: a capability method physically cannot run its effect without
// gating first, nor return a result on a denied call — bypass is impossible by
// construction. This keeps a capability's guarded pipeline (authorize → its own
// leak-scan / credential-inject / execute) cohesive while making a forgotten or
// out-of-order gate unrepresentable. A free function, not a method, so it can be
// generic over the effect's result type.
func Do[T any](ctx context.Context, g *Guard, call capability.Call, intent string, effect func() (T, error)) (T, error) {
	if err := g.Authorize(ctx, call, intent); err != nil {
		var zero T
		return zero, err
	}
	return effect()
}
