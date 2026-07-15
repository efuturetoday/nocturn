// Package capability decides whether a guest's attempt to use a capability is
// permitted. It is the broker that sits in front of every host-function window:
// a guest may reach the outside world only through a capability, and only if
// the policy says so.
//
// This layer is pure decision logic — no wazero, no I/O, no HITL. It answers
// one question: given a call and a policy, is it Allow, Ask, or Deny?
package capability

import (
	"path"
	"time"
)

// Wildcard is the explicit "match any" token for Rule.Capability and
// Rule.TargetGlob. An empty field never means "any" — use Wildcard so intent is
// always visible and a forgotten field fails closed.
const Wildcard = "*"

// Decision is the broker's verdict on a capability call.
type Decision int

const (
	// Deny blocks the call. It is both the default (nothing is permitted unless
	// a rule says so) and the winner (a matching Deny beats any Allow/Ask).
	Deny Decision = iota
	// Ask requires out-of-band human approval before the call may proceed.
	// The approval mechanism itself is a later layer (HITL); here Ask is only
	// the verdict.
	Ask
	// Allow permits the call.
	Allow
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "Allow"
	case Ask:
		return "Ask"
	default:
		return "Deny"
	}
}

// Call describes a capability invocation for the broker to evaluate. Target is
// the capability-defined resource string the call acts on — the host for http,
// a path for file.*, a command for exec — matched against Rule.TargetGlob. A
// pure-compute or targetless capability (e.g. "log") leaves it "".
type Call struct {
	Capability string // e.g. "log", "http.read", "file.write"
	Target     string // e.g. "api.example.com", "/work/notes.md"; "" = targetless
}

// Rule matches calls and assigns an effect. Wildcards are always explicit "*"
// — an empty field never means "any", so a half-filled rule fails closed
// instead of silently granting everything.
//
//	Capability: "*" matches any capability; a name matches exactly; ""
//	            (empty) matches nothing.
//	TargetGlob: "*" matches any target; a shell glob (path.Match), e.g.
//	            "*.example.com" or "/work/notes/*", matches that target; ""
//	            (empty) is NOT target-scoped and matches only targetless calls
//	            (e.g. "log") — it never matches a target-bearing call. This makes
//	            it impossible to allow every target by forgetting to set one:
//	            "any target" must be written explicitly as "*". path.Match's "*"
//	            does not cross "/", so a path glob is depth-bounded for free.
//	Epoch:      the zero value is unset and matches NOTHING (fail closed) —
//	            permanence is never implicit. Use Permanent for a grant that
//	            never expires, or an id from EpochRegistry.Open to bind the
//	            grant to a task: it then matches only while that epoch is alive
//	            (via Evaluate with a live Env.Epochs), so revoking the epoch
//	            kills the grant.
//	Window:     nil is not time-constrained; a *Window restricts the rule to a
//	            daily time range (checked against Env.Now).
type Rule struct {
	Capability string
	TargetGlob string
	Effect     Decision
	Epoch      EpochID
	Window     *Window
}

func (r Rule) matches(call Call, env Env) bool {
	switch r.Capability {
	case "":
		return false // fail closed: an empty capability grants nothing
	case Wildcard:
		// matches any capability
	default:
		if r.Capability != call.Capability {
			return false
		}
	}

	hasTarget := call.Target != ""
	switch {
	case r.TargetGlob == "":
		// Not target-scoped: only matches targetless calls (e.g. "log").
		if hasTarget {
			return false
		}
	case !hasTarget:
		// Target-scoped rule cannot match a call that carries no target.
		return false
	case r.TargetGlob == Wildcard:
		// Explicit match-any: covers ANY target, including multi-segment paths
		// (path.Match's "*" would not cross "/", but Wildcard is our any-token, not
		// a glob). A target must still be present (handled above).
	default:
		if ok, err := path.Match(r.TargetGlob, call.Target); err != nil || !ok {
			return false
		}
	}

	// Epoch scoping. The zero value is unset and matches nothing (fail closed);
	// Permanent never expires; any other id must be alive in the registry.
	switch r.Epoch {
	case 0:
		return false
	case Permanent:
		// never expires
	default:
		if env.Epochs == nil || !env.Epochs.IsAlive(r.Epoch) {
			return false
		}
	}

	// Time window: a windowed rule only matches within its daily range.
	if r.Window != nil && !r.Window.contains(env.Now) {
		return false
	}
	return true
}

// Policy is a set of rules evaluated with deny-by-default.
type Policy struct {
	Rules []Rule
}

// Env carries the contextual inputs the broker consults during evaluation, all
// optional:
//
//	Now       current time, for Rule.Window checks (zero time = outside any
//	          window, so windowed rules fail closed without a real clock).
//	Epochs    the epoch registry, for revocation (nil = epoch-scoped grants
//	          fail closed).
//	RateAllow a predicate that reports (and records) whether a call is within
//	          its rate budget (nil = no rate limiting).
type Env struct {
	Now       time.Time
	Epochs    *EpochRegistry
	RateAllow func(Call) bool
}

// Evaluate returns the decision for a call in env: deny > ask > allow precedence
// over rules whose match conditions (capability, target, live epoch, time window)
// all hold, then a rate-limit post-check that turns a would-be Allow into Deny
// when the call is over budget. Pass a zero Env{} for a context-free evaluation
// — epoch-scoped and windowed grants then fail closed.
func (p Policy) Evaluate(call Call, env Env) Decision {
	d := p.decide(call, env)
	if d == Allow && env.RateAllow != nil && !env.RateAllow(call) {
		return Deny // over the rate budget: hard cap
	}
	return d
}

// decide applies deny > ask > allow precedence with deny-by-default: if any
// matching rule is Deny the result is Deny; else Ask; else Allow; else Deny.
// Deny always wins, and absence of any match means Deny — the guest gets
// nothing it was not explicitly granted.
func (p Policy) decide(call Call, env Env) Decision {
	anyMatches := func(want Decision) bool {
		for _, r := range p.Rules {
			if r.Effect == want && r.matches(call, env) {
				return true
			}
		}
		return false
	}
	switch {
	case anyMatches(Deny):
		return Deny
	case anyMatches(Ask):
		return Ask
	case anyMatches(Allow):
		return Allow
	default:
		return Deny
	}
}
