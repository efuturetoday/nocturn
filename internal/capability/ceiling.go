package capability

import "context"

// A Ceiling is an upper bound on what a caller may ATTEMPT: a set of allowed
// (capability, target-glob) pairs. It is not a grant — it never turns Ask into
// Allow; it only permits an effect to be attempted (and thus asked about).
// Outside the ceiling an effect is hard-denied and never even reaches HITL,
// which is what stops a prompt-injected caller from getting you to approve
// something it was never allowed to try.
//
// Ceilings compose by INTERSECTION: an effect is attemptable only if EVERY
// ceiling in scope allows it (see CeilingsFrom). A workspace declares an outer
// ceiling for all callers; a plugin declares an inner one from its manifest;
// further levels (user-global kill-switch, per-agent, …) just add more links to
// the chain — the composition rule never changes.
type Ceiling struct {
	policy Policy
}

// Pair is one allowed reach entry of a Ceiling: a Family + target-glob, plus the
// Writes axis (the Schreibrecht — whether this reach permits reads, writes, or
// both). The same fail-closed semantics as Rule apply: an empty Family/TargetGlob
// matches nothing, a target-bearing call needs an explicit glob ("*" for any), and
// the zero Writes (MatchNone) permits nothing — so a pair that forgets to declare
// read/write grants no reach at all.
type Pair struct {
	Family     string
	TargetGlob string
	Writes     Match
}

// NewCeiling builds a ceiling allowing exactly the given pairs (each a Permanent
// Allow rule). No pairs → allows nothing (fail closed).
func NewCeiling(pairs ...Pair) Ceiling {
	rules := make([]Rule, 0, len(pairs))
	for _, p := range pairs {
		rules = append(rules, Rule{
			Family:     p.Family,
			TargetGlob: p.TargetGlob,
			Writes:     p.Writes,
			Effect:     Allow,
			Epoch:      Permanent,
		})
	}
	return Ceiling{policy: Policy{Rules: rules}}
}

// Allows reports whether call is within this ceiling.
func (c Ceiling) Allows(call Call) bool {
	return c.policy.Evaluate(call, Env{}) == Allow
}

// The ceiling chain travels through the request context (like the epoch), so a
// nested scope can add its own ceiling without widening signatures down the tool
// chain. Each WithCeiling APPENDS; CeilingsFrom returns the whole chain.

type ceilingKey struct{}

// WithCeiling returns a context whose ceiling chain has c appended. Every
// authorization intersects the whole chain — a call must satisfy all of them.
func WithCeiling(ctx context.Context, c Ceiling) context.Context {
	chain := CeilingsFrom(ctx)
	next := make([]Ceiling, len(chain)+1)
	copy(next, chain)
	next[len(chain)] = c
	return context.WithValue(ctx, ceilingKey{}, next)
}

// CeilingsFrom returns the ceiling chain carried by ctx (nil if none — an
// unrestricted caller such as the model or a bare script).
func CeilingsFrom(ctx context.Context) []Ceiling {
	chain, _ := ctx.Value(ceilingKey{}).([]Ceiling)
	return chain
}

// WithinCeilings reports whether call satisfies EVERY ceiling in ctx's chain
// (vacuously true if there is none). This is the Guard's hard upper-bound check.
func WithinCeilings(ctx context.Context, call Call) bool {
	for _, c := range CeilingsFrom(ctx) {
		if !c.Allows(call) {
			return false
		}
	}
	return true
}
