package gate

import (
	"context"
	"errors"
	"fmt"

	"github.com/efuturetoday/nocturn/agentkit"
)

// ErrDenied is returned by Check when an action is not permitted — a policy Deny, an Ask with no
// covering grant and no Approver (or an unattended session), or a human declining. It is surfaced to
// the model as the tool result, so the model adjusts instead of the turn crashing.
var ErrDenied = errors.New("gate: denied")

type permsKey struct{}

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

	ruling := Allowed()
	if p.policy != nil {
		ruling = p.policy.Decide(a)
	}
	switch ruling.decision {
	case decisionAllow:
		return nil
	case decisionDeny:
		return ErrDenied
	}

	// Ask. A standing grant covers it — unless this Kind is never remembered, in which case the cache
	// is skipped and a human must approve every time.
	if ruling.recall != RecallNever && p.grants != nil && p.grants.Allowed(a, match) {
		return nil
	}
	if p.approver == nil {
		return ErrDenied // unattended: no human to approve
	}

	resume := agentkit.Pause(ctx) // a human deciding must not consume the turn's wall-clock
	approved, g, chosen, err := p.approver.Ask(ctx, a, suggest)
	resume()
	if err != nil {
		return fmt.Errorf("gate: approver: %w", err)
	}
	if !approved {
		return ErrDenied
	}

	// Remember the (possibly widened) grant at the more restrictive of the policy's ceiling and the
	// human's choice; RecallNever means don't remember (asks again next time).
	if effective := min(ruling.recall, chosen); effective != RecallNever && p.grants != nil {
		p.grants.Remember(g, effective)
	}
	return nil
}
