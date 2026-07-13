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

	mu      sync.Mutex
	session []capability.Rule // grants remembered via "Allow this session"
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
// "Allow this session" additionally remembers the grant so the same capability
// and host is not asked again.
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
		// explicit Deny (deny-wins stays a hard rail).
		if g.sessionAllows(call, env) {
			return nil
		}
		out, err := g.Approvals.Request(ctx, intent, approvalChoices, g.TTL)
		if err != nil {
			return err
		}
		switch out {
		case hitl.ApprovedSession:
			g.allowSession(call)
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

// sessionAllows reports whether a call is covered by a remembered session grant.
func (g *Guard) sessionAllows(call capability.Call, env capability.Env) bool {
	g.mu.Lock()
	sess := g.session
	g.mu.Unlock()
	if len(sess) == 0 {
		return false
	}
	return capability.Policy{Rules: sess}.Evaluate(call, env) == capability.Allow
}

// allowSession remembers a grant for the rest of the session, so the same
// capability + host is auto-allowed without asking again.
func (g *Guard) allowSession(call capability.Call) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.session = append(g.session, capability.Rule{
		Capability: call.Capability,
		HostGlob:   call.Attrs["host"], // exact host; "" matches hostless calls
		Effect:     capability.Allow,
		Epoch:      capability.Permanent,
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
