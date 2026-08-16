package gate

import "context"

// Approver presents an action — plus a set of SUGGESTED grants the calling tool proposes as sensible
// widenings (e.g. "*.github.com" for a host, or a parent directory for a path) — to a human
// out-of-band. It returns whether the human approved, the Grant to remember if so (the exact action,
// a suggestion, or whatever the human chose), and how long it holds. When approved is false the Grant
// and Scope are ignored. A nil Approver means unattended: an action that would be asked is denied
// (fail-closed). The suggestions are target-semantic and come from the tool, not from this library or
// the approver. Implementations block until the human answers or ctx is cancelled; the caller pauses
// the turn's wall-clock around the wait.
//
// ceiling is the policy's cap on how long an approval may be remembered, and an implementation MUST
// NOT offer an answer above it. Check clamps the returned recall anyway, so ignoring the ceiling is
// not a security hole — it is a HONESTY hole, which is worse in the way that matters: a sheet
// offering "always" for an action capped at RecallNever takes a person's standing yes, silently
// downgrades it to this once, and asks them again tomorrow. They answered a question that was never
// on offer. Offerable is the shared rule for filtering.
type Approver interface {
	Ask(ctx context.Context, a Action, ceiling Recall, suggest []Grant) (approved bool, remember Grant, recall Recall, err error)
}

// Offerable reports the recall an answer may carry under ceiling, and whether to offer it at all. It
// is the WHOLE rule, in one place, because every approver renders its own answer sheet and a rule
// split across two of them drifts the day it changes.
//
// The two kinds of answer are treated differently, and that asymmetry is the substance:
//
// An answer on the action's EXACT target is offered unchanged or not at all. Clamping "always" down
// to a session under a session ceiling would put two buttons on the sheet that do the same thing,
// with the upper one lying about it. So above the ceiling it is dropped, and what remains is what it
// says. "Allow once" is RecallNever and therefore survives every ceiling — which is the point: there
// is always at least one answer.
//
// A WIDENING is clamped instead, because its purpose is the broader TARGET and the recall is
// secondary: "everyone at this domain, for this session" is a real answer somebody may want. The
// exception is a RecallNever ceiling, where nothing is remembered at all — a broader grant that
// outlives nothing is the "once" button wearing a bigger label, and offering it would be the same
// lie in a wider frame.
func Offerable(want, ceiling Recall, widens bool) (Recall, bool) {
	if !widens {
		return want, want <= ceiling
	}
	if ceiling == RecallNever {
		return RecallNever, false
	}
	return min(want, ceiling), true
}
