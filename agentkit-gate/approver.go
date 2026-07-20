package gate

import "context"

// Approver presents an action to a human out-of-band and returns their decision and how long it
// holds. A nil Approver means unattended: an action that would be asked is denied instead
// (fail-closed — no human is present to approve). Implementations block until the human answers or
// ctx is cancelled; the caller pauses the turn's wall-clock around the wait.
type Approver interface {
	Ask(ctx context.Context, a Action) (Decision, Scope, error)
}
