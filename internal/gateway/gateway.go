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
	"github.com/efuturetoday/nocturn/internal/tool"
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

// approvalChoicesFor builds the options a human is offered on Ask, labelling the
// session/always choices with EXACTLY what would be remembered — the model-facing
// tool (and, for "always", the target). The label must name the grant's real
// scope: a human who reads "Send email to Bob" must see that "always" remembers
// "gmail.send @ gmail.googleapis.com" (any recipient), never a bare "Allow always"
// that hides a broad standing grant behind a narrow prompt.
func approvalChoicesFor(toolName string, call capability.Call) []hitl.Choice {
	name := toolName
	if name == "" {
		name = call.Family // native/direct call: the primitive family is the tool
	}
	always := "Allow always: " + name
	if call.Target != "" {
		always += " @ " + call.Target
	}
	return []hitl.Choice{
		{Label: "Allow once", Outcome: hitl.Approved},
		{Label: "Allow this session: " + name, Outcome: hitl.ApprovedSession},
		{Label: always, Outcome: hitl.ApprovedAlways},
		{Label: "Deny", Outcome: hitl.Denied},
	}
}

// consequentialChoices are the only options for a never-auto (consequential) effect:
// once or deny — never a standing grant.
var consequentialChoices = []hitl.Choice{
	{Label: "Allow once", Outcome: hitl.Approved},
	{Label: "Deny", Outcome: hitl.Denied},
}

// factLine is the unforgeable, host-computed line shown beneath a semantic intent
// at every Ask: the model-facing tool and the reconstructed (capability, target)
// it actually reaches. It comes only from the Call the host built — never from
// guest or model text — so a semantic template can NEVER hide what is really
// gated. Empty target (a targetless call like "log") omits the "@ target".
func factLine(toolName string, call capability.Call) string {
	name := toolName
	if name == "" {
		name = call.Family
	}
	op := call.Family + " " + opWord(call.Mutates)
	if call.Target != "" {
		return "via " + name + " → " + op + " @ " + call.Target
	}
	return "via " + name + " → " + op
}

// opWord renders the mutation axis for a human: reads vs writes.
func opWord(mutates bool) string {
	if mutates {
		return "write"
	}
	return "read"
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
		env.RateAllow = func(c capability.Call) bool { return g.Rate.Allow(c.Family) }
	}

	// The never-auto floor: a consequential (irreversible/high-stakes) effect ALWAYS
	// asks out of band — it can never be auto-allowed by policy, covered by a standing
	// grant, or granted for a session/always. Stamped by a trusted layer (the plugin
	// Host, from an install-reviewed ToolDecl), never by guest code.
	consequential := consequentialFrom(ctx)

	// Compose the verdict from the workspace base policy UNIONed with any scope
	// (agent) policy carried on ctx — deny>ask>allow, so a scope can tighten (deny
	// blacklist, or ask where the base allows). Loosening is the deferred autonomy
	// dial (see capability.WithPolicy).
	policy := g.Policy
	if extra := capability.PolicyRulesFrom(ctx); len(extra) > 0 {
		policy = capability.Policy{Rules: append(append([]capability.Rule(nil), g.Policy.Rules...), extra...)}
	}
	decision := policy.Evaluate(call, env)
	if consequential && decision == capability.Allow {
		decision = capability.Ask // escalate: consequential is never auto
	}

	switch decision {
	case capability.Allow:
		return nil
	case capability.Ask:
		grants := capability.GrantsFrom(ctx)
		toolName := tool.ToolName(ctx) // the model-facing tool a grant is remembered against
		if !consequential && grants != nil && grants.Allows(toolName, call, env) {
			// A standing grant answers the Ask — but still respects the rate cap, so a
			// remembered "always" cannot be replayed without bound (the grant path used
			// to bypass the limiter).
			if env.RateAllow != nil && !env.RateAllow(call) {
				return ErrDenied
			}
			return nil // standing grant (session or always), scoped to this tool
		}
		// A higher, trusted layer (a plugin Host rendering an install-reviewed
		// manifest template) may have stamped a semantic intent onto ctx — show it
		// as the human-readable HEAD so the human reads "Send email to x@a". But the
		// host-computed fact line ALWAYS rides beneath it (never replaced), so a
		// semantic template can't hide the real (capability, target) being gated.
		prompt := intent
		if r := intentFrom(ctx); r != "" {
			prompt = r + "\n" + factLine(toolName, call)
		}
		// A consequential effect offers only once-or-deny: no "session"/"always", so it
		// can never become a standing grant.
		choices := approvalChoicesFor(toolName, call)
		if consequential {
			choices = consequentialChoices
		}
		out, err := g.Approvals.Request(ctx, prompt, choices, g.TTL)
		if err != nil {
			return err
		}
		switch out {
		case hitl.ApprovedAlways:
			if grants != nil && !consequential {
				_ = grants.Record(toolName, call, capability.ScopeAlways) // persist error must not block the allow
			}
			return nil
		case hitl.ApprovedSession:
			if grants != nil && !consequential {
				_ = grants.Record(toolName, call, capability.ScopeSession)
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

// intentKey carries a higher layer's semantic intent for the current operation.
type intentKey struct{}

// WithIntent attaches a human-readable intent that Authorize prefers over the
// effect tool's own (transport-level) intent when prompting. It is set by a
// TRUSTED layer only — the plugin Host, rendering a template that was reviewed at
// install time — never by guest code, so a plugin cannot forge a misleading
// prompt for an effect it isn't allowed to attempt (the ceiling still bounds the
// real target, and that is what is gated).
func WithIntent(ctx context.Context, intent string) context.Context {
	return context.WithValue(ctx, intentKey{}, intent)
}

func intentFrom(ctx context.Context) string {
	s, _ := ctx.Value(intentKey{}).(string)
	return s
}

// consequentialKey marks the current operation as never-auto (see ToolDecl.Consequential).
type consequentialKey struct{}

// WithConsequential marks the current operation as consequential (irreversible/
// high-stakes): Authorize will always ask out of band and never let a standing
// grant answer or be recorded. Set by a TRUSTED layer only (the plugin Host, from
// an install-reviewed manifest flag), never by guest code — so a plugin cannot mark
// its own effect as NON-consequential to dodge the floor (absence is the default,
// and the flag only ever tightens).
func WithConsequential(ctx context.Context) context.Context {
	return context.WithValue(ctx, consequentialKey{}, true)
}

func consequentialFrom(ctx context.Context) bool {
	v, _ := ctx.Value(consequentialKey{}).(bool)
	return v
}
