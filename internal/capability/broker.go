// Package capability decides whether a guest's attempt to use a capability is
// permitted. It is the broker that sits in front of every host-function window:
// a guest may reach the outside world only through a capability, and only if
// the policy says so.
//
// This layer is pure decision logic — no wazero, no I/O, no HITL. It answers
// one question: given a call and a policy, is it Allow, Ask, or Deny?
package capability

import (
	"context"
	"path"
	"time"
)

// Wildcard is the explicit "match any" token for Rule.Family and Rule.TargetGlob.
// An empty field never means "any" — use Wildcard so intent is always visible and
// a forgotten field fails closed.
const Wildcard = "*"

// Match selects which mutation class a Rule applies to — the write axis, kept
// separate from reachability (family + target). The zero value MatchNone matches
// NOTHING (fail closed): a rule or cage entry that forgets to set it grants no
// authority, so "may write" is never implicit. This is the Schreibrecht on a
// cage pair and the read/write selector on a policy rule.
type Match int

const (
	// MatchNone is the fail-closed zero: it matches neither reads nor writes.
	MatchNone Match = iota
	// MatchRead matches only non-mutating (read/safe) calls.
	MatchRead
	// MatchWrite matches only mutating (write) calls.
	MatchWrite
	// MatchAny matches both reads and writes (full read+write reach).
	MatchAny
)

// covers reports whether m applies to a call with the given mutation flag.
func (m Match) covers(write bool) bool {
	switch m {
	case MatchAny:
		return true
	case MatchWrite:
		return write
	case MatchRead:
		return !write
	default:
		return false // MatchNone (and any invalid value) fails closed
	}
}

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

// Call describes a capability invocation for the broker to evaluate on two
// orthogonal axes (see KONZEPT-sicherheit-ux.md §3):
//
//   - REACH: Family (the host primitive — "http", "file", "dns") + Target (the
//     family-defined resource: a host for http, a path for file; "" = targetless).
//   - WIRKUNG: Write — whether the call changes the world (false = read/safe,
//     true = write/mutating). Derived host-side from the real operation (an HTTP
//     method, a read-vs-write file op), never trusted from the tool's name.
//
// Splitting these lets a cage gate reachability (which hosts, and whether
// writes are permitted at all) while the policy gates read/write (reads auto,
// writes ask) — instead of baking read/write into the capability name.
type Call struct {
	Family string // e.g. "log", "http", "file", "dns"
	Write  bool   // false = read (safe), true = write (mutating)
	Target string // e.g. "api.example.com", "notes/x.md"; "" = targetless
}

// Rule matches calls and assigns an effect. Wildcards are always explicit "*"
// — an empty field never means "any", so a half-filled rule fails closed
// instead of silently granting everything.
//
//	Family:     "*" matches any family; a name matches exactly; "" (empty)
//	            matches nothing.
//	Writes:     which mutation class the rule applies to (MatchRead/Write/Any);
//	            the zero MatchNone matches nothing (fail closed) — "may write" is
//	            never implicit.
//	TargetGlob: "*" matches any target; a shell glob (path.Match), e.g.
//	            "*.example.com" or "notes/*", matches that target; "" (empty) is
//	            NOT target-scoped and matches only targetless calls (e.g. "log") —
//	            it never matches a target-bearing call. This makes it impossible to
//	            allow every target by forgetting to set one: "any target" must be
//	            written explicitly as "*". path.Match's "*" does not cross "/", so a
//	            path glob is depth-bounded for free.
//	Epoch:      the zero value is unset and matches NOTHING (fail closed) —
//	            permanence is never implicit. Use Permanent for a grant that
//	            never expires, or an id from EpochRegistry.Open to bind the
//	            grant to a task: it then matches only while that epoch is alive
//	            (via Evaluate with a live Env.Epochs), so revoking the epoch
//	            kills the grant.
//	Window:     nil is not time-constrained; a *Window restricts the rule to a
//	            daily time range (checked against Env.Now).
type Rule struct {
	Family     string
	TargetGlob string
	Writes     Match
	Effect     Decision
	Epoch      EpochID
	Window     *Window
}

func (r Rule) matches(call Call, env Env) bool {
	switch r.Family {
	case "":
		return false // fail closed: an empty family grants nothing
	case Wildcard:
		// matches any family
	default:
		if r.Family != call.Family {
			return false
		}
	}

	if !r.Writes.covers(call.Write) {
		return false // wrong mutation class (read vs write)
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

// A scope (an agent run) may layer its OWN policy rules onto the workspace base —
// author-declared standing intent, NOT runtime grants. They travel through ctx like
// the cage and compose by flat UNION with the base under deny>ask>allow: so a
// scope can TIGHTEN (add Deny = a blacklist, deny-wins; add Ask where the base
// allows, ask-beats-allow) immediately. LOOSENING (Allow where the base asks) does
// NOT work by union (ask beats allow) and needs a precedence layer — that is the
// autonomy dial, deferred (see KONZEPT §9). Grants never travel here; they are
// runtime consent, consulted only after a policy Ask.

type policyRulesKey struct{}

// WithPolicy returns a ctx whose scoped-policy chain has p's rules appended. Every
// authorization unions these with the guard's base policy.
func WithPolicy(ctx context.Context, p Policy) context.Context {
	if len(p.Rules) == 0 {
		return ctx
	}
	prev := PolicyRulesFrom(ctx)
	next := make([]Rule, 0, len(prev)+len(p.Rules))
	next = append(next, prev...)
	next = append(next, p.Rules...)
	return context.WithValue(ctx, policyRulesKey{}, next)
}

// PolicyRulesFrom returns the scoped policy rules carried by ctx (nil if none).
func PolicyRulesFrom(ctx context.Context) []Rule {
	rules, _ := ctx.Value(policyRulesKey{}).([]Rule)
	return rules
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
