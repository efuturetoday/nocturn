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
type Approver interface {
	Ask(ctx context.Context, a Action, suggest []Grant) (approved bool, remember Grant, recall Recall, err error)
}
