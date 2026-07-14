// Package gateway is the guarded path to real-world effects.
//
// Its core is the Guard: the one reusable authorization pipeline — the broker
// decision (allow / ask / deny, with epoch, time window, and rate limit) plus
// out-of-band human approval on Ask. Capabilities do NOT accumulate on a single
// growing struct: each capability group (e.g. Net) is a small type that holds a
// *Guard and its own dependencies, and runs every call through Guard.Authorize
// before performing any effect. Adding a capability (dns, ping, email) is a new
// small type or method — never a new field on a god-object.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// ErrDenied is returned when a capability call is not permitted (broker Deny, or
// a human denied / did not approve in time).
var ErrDenied = errors.New("gateway: capability denied")

// Guard authorizes capability calls. It is host-trusted and shared by every
// capability group.
type Guard struct {
	Policy    capability.Policy
	Approvals *hitl.Engine
	Epochs    *capability.EpochRegistry
	Rate      *capability.RateLimiter
	TTL       time.Duration
	Now       func() time.Time

	mu     sync.Mutex
	grants []capability.Rule // "Allow this session" grants, each bound to a session epoch
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
	{Label: "Deny", Outcome: hitl.Denied},
}

// Authorize runs a call through the broker and, on Ask, through out-of-band
// human approval. It returns nil if the call may proceed, ErrDenied otherwise.
// "Allow this session" additionally remembers the grant, bound to the epoch
// carried by ctx, so the same capability and host is not asked again — until
// that epoch is closed, which revokes the grant.
func (g *Guard) Authorize(ctx context.Context, call capability.Call, intent string) error {
	env := capability.Env{Now: g.now(), Epochs: g.Epochs}
	if g.Rate != nil {
		env.RateAllow = func(c capability.Call) bool { return g.Rate.Allow(c.Capability) }
	}

	switch g.Policy.Evaluate(call, env) {
	case capability.Allow:
		return nil
	case capability.Ask:
		// A session grant short-circuits the ask — but only an Ask, never an
		// explicit Deny (deny-wins stays a hard rail). The grant is epoch-aware:
		// a grant whose epoch has been closed no longer matches.
		if g.sessionAllows(call, env) {
			return nil
		}
		out, err := g.Approvals.Request(ctx, intent, approvalChoices, g.TTL)
		if err != nil {
			return err
		}
		switch out {
		case hitl.ApprovedSession:
			g.grant(capability.EpochFrom(ctx), call)
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
func Do[T any](g *Guard, ctx context.Context, call capability.Call, intent string, effect func() (T, error)) (T, error) {
	if err := g.Authorize(ctx, call, intent); err != nil {
		var zero T
		return zero, err
	}
	return effect()
}

// sessionAllows reports whether a call is covered by a live session grant. It
// evaluates the grants with the same Env, so a grant bound to a closed epoch
// (IsAlive == false) fails to match — revocation for free.
func (g *Guard) sessionAllows(call capability.Call, env capability.Env) bool {
	g.mu.Lock()
	grants := g.grants
	g.mu.Unlock()
	if len(grants) == 0 {
		return false
	}
	return capability.Policy{Rules: grants}.Evaluate(call, env) == capability.Allow
}

// grant remembers a call's capability + host as allowed for the given epoch, so
// the same call is auto-allowed without asking again while that epoch is alive.
// Closing the epoch (EpochRegistry.Close) revokes it. Dead-epoch grants are
// pruned on the way in, so the slice cannot grow across revoked sessions.
func (g *Guard) grant(epoch capability.EpochID, call capability.Call) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Epochs != nil {
		kept := g.grants[:0]
		for _, r := range g.grants {
			if g.Epochs.IsAlive(r.Epoch) {
				kept = append(kept, r)
			}
		}
		g.grants = kept
	}
	g.grants = append(g.grants, capability.Rule{
		Capability: call.Capability,
		HostGlob:   call.Attrs["host"], // exact host; "" matches hostless calls
		Effect:     capability.Allow,
		Epoch:      epoch,
	})
}

func hostOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("gateway: bad url %q: %w", rawURL, err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("gateway: url %q has no host", rawURL)
	}
	return u.Hostname(), nil
}
