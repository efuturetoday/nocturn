package capability

import "context"

// Autonomy is how an Ask verdict is resolved when NO human is at the console — an
// unattended (scheduled/webhook) run. It changes nothing about the cage or deny
// rails (those still bound and win); it only decides what an otherwise-live Ask
// becomes. The zero value is AutonomyAttended: a human IS present (a manual run),
// so an Ask asks normally — the dial is inert unless a caller stamps otherwise.
//
// The dial exists because an unattended run has no one to answer a prompt. Rather
// than a blanket auto-approve (OpenClaw/IronClaw's unsafe default), the level is an
// explicit, per-agent trust choice:
//
//	AutonomyAttended — a human is here (manual/TUI). Normal HITL. (zero value)
//	AutonomyGuarded  — unattended, but the out-of-band channel still reaches a human
//	                   (the phone): an Ask ASKS, the task pauses until approve/deny/TTL.
//	                   The safe default for scheduled runs — the moat, unattended.
//	AutonomyStrict   — unattended: an Ask is DENIED. Never act without a human present.
//	AutonomyFull     — unattended: an Ask AUTO-ALLOWS within the cage+policy (no human).
//	                   A consequential effect still asks — the never-auto floor wins.
type Autonomy int

const (
	AutonomyAttended Autonomy = iota
	AutonomyGuarded
	AutonomyStrict
	AutonomyFull
)

type autonomyKey struct{}

// WithAutonomy stamps the run's autonomy level onto ctx. A scheduler stamps it for
// an unattended run; an interactive (manual) run leaves it unset (AutonomyAttended).
func WithAutonomy(ctx context.Context, a Autonomy) context.Context {
	return context.WithValue(ctx, autonomyKey{}, a)
}

// AutonomyFrom returns the run's autonomy level (AutonomyAttended if unset — a human
// is assumed present, so HITL behaves normally).
func AutonomyFrom(ctx context.Context) Autonomy {
	a, _ := ctx.Value(autonomyKey{}).(Autonomy)
	return a
}
