package gate

import (
	"context"
	"errors"
	"fmt"

	"github.com/efuturetoday/nocturn/agentkit"
)

// ErrDenied is the umbrella "not permitted" error: every deny below wraps it, so
// errors.Is(err, ErrDenied) still matches any denial. The reason-specific sentinels let a caller
// branch on WHY — e.g. an unattended deny might be retried attended, a policy deny never should. The
// error is surfaced to the model as the tool result, so the model adjusts instead of the turn crashing.
var ErrDenied = errors.New("gate: denied")

// Reason-specific denials, each wrapping ErrDenied:
var (
	// ErrDeniedPolicy — the policy returned Deny for this action.
	ErrDeniedPolicy = fmt.Errorf("policy deny: %w", ErrDenied)
	// ErrDeniedUnattended — an Ask with no covering grant and no approver (a fired/background agent
	// has no human to approve), so it fails closed.
	ErrDeniedUnattended = fmt.Errorf("unattended, no approver: %w", ErrDenied)
	// ErrDeniedDeclined — a human was asked and declined.
	ErrDeniedDeclined = fmt.Errorf("declined by human: %w", ErrDenied)
)

type permsKey struct{}
type gateLogKey struct{}

// perms is the installed machinery, carried in ctx.
type perms struct {
	policy   Policy
	grants   Grants
	approver Approver
}

// With installs the permission machinery into ctx: a Policy, a Grants store and an Approver (nil =
// unattended). It flows to nested tool calls and sub-agents through ctx, so one install covers the
// whole tree; a nested run inherits it and cannot widen it (only a human, via the Approver, widens).
func With(ctx context.Context, p Policy, g Grants, a Approver) context.Context {
	return context.WithValue(ctx, permsKey{}, &perms{policy: p, grants: g, approver: a})
}

// WithLogger installs the logger that Check traces every decision through — kept separate from With
// so the permission signature stays stable. A nil logger (the default when unset) traces nothing.
func WithLogger(ctx context.Context, log agentkit.Logger) context.Context {
	return context.WithValue(ctx, gateLogKey{}, log)
}

// loggerFrom returns the installed gate logger, or a no-op when none is set.
func loggerFrom(ctx context.Context) agentkit.Logger {
	if l, ok := ctx.Value(gateLogKey{}).(agentkit.Logger); ok && l != nil {
		return l
	}
	return agentkit.NopLogger()
}

func from(ctx context.Context) *perms {
	p, _ := ctx.Value(permsKey{}).(*perms)
	return p
}

// Check gates one action against the installed machinery:
//
//	Policy Allow -> nil
//	Policy Deny  -> ErrDenied
//	Policy Ask   -> a covering Grant passes; else the Approver is asked out-of-band (the turn's
//	                wall-clock is paused via agentkit.Pause while the human decides) and the answer is
//	                remembered at the more restrictive of the policy's Recall and the human's choice.
//
// No machinery in ctx = open (returns nil): gating is opt-in per install. A tool calls Check before a
// targeted action (Action{Kind: "net", Target: host}), passing the Matcher for that axis's target
// semantics and any suggested widenings the human may pick (e.g. Grant{"net", "*.example.com"}); Wrap
// calls it for a name-only tool with a nil matcher and no suggestions.
func Check(ctx context.Context, a Action, match Matcher, suggest ...Grant) error {
	p := from(ctx)
	if p == nil {
		return nil // no machinery installed = open
	}
	// Decision tracing: every allow/deny/ask/remember is logged so the human-in-the-loop core is not
	// a black box. Only the action's Kind/Target (never any secret) reach the log.
	lg := loggerFrom(ctx).WithContext(ctx).With("component", "gate", "kind", a.Kind, "target", a.Target)

	ruling := Allowed()
	if p.policy != nil {
		ruling = p.policy.Decide(a)
	}
	switch ruling.decision {
	case decisionAllow:
		lg.Debug("gate allow", "source", "policy")
		return nil
	case decisionDeny:
		lg.Warn("gate deny", "reason", "policy")
		return ErrDeniedPolicy
	}

	// Ask. A standing grant covers it — unless this Kind is never remembered, in which case the cache
	// is skipped and a human must approve every time.
	if ruling.recall != RecallNever && p.grants != nil && p.grants.Allowed(a, match) {
		lg.Debug("gate allow", "source", "grant")
		return nil
	}
	if p.approver == nil {
		lg.Warn("gate deny", "reason", "unattended") // no human to approve — fail closed
		return ErrDeniedUnattended
	}

	lg.Debug("gate ask", "recall", ruling.recall)
	resume := agentkit.Pause(ctx) // a human deciding must not consume the turn's wall-clock
	approved, g, chosen, err := p.approver.Ask(ctx, a, suggest)
	resume()
	if err != nil {
		lg.Warn("gate deny", "reason", "approver-error", "err", err)
		return fmt.Errorf("gate: approver: %w", err)
	}
	if !approved {
		lg.Warn("gate deny", "reason", "declined")
		return ErrDeniedDeclined
	}

	// Remember the (possibly widened) grant at the more restrictive of the policy's ceiling and the
	// human's choice; RecallNever means don't remember (asks again next time).
	if effective := min(ruling.recall, chosen); effective != RecallNever && p.grants != nil {
		p.grants.Remember(g, effective)
		lg.Info("gate grant remembered", "grant_target", g.Target, "recall", effective)
	}
	lg.Debug("gate allow", "source", "approved")
	return nil
}
