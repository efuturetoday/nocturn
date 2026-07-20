package gate

// Decision is a policy outcome. The zero value is Ask — fail-safe: an unclassified action is asked,
// never silently allowed.
type Decision int

const (
	Ask Decision = iota
	Allow
	Deny
)

func (d Decision) String() string {
	switch d {
	case Ask:
		return "ask"
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	default:
		return "unknown"
	}
}

// Scope is how long an approval holds (what the human chose).
type Scope int

const (
	Once   Scope = iota // this session only
	Always              // remembered durably
)

// Recall caps, per Kind, how long an approval MAY be remembered — the policy's ceiling on the human's
// choice. It bounds irreversible actions: a pay/delete Kind can require RecallNever so it is asked
// every single time, no matter what the human picks.
type Recall int

const (
	RecallAlways  Recall = iota // may be remembered durably (the human's Always is honored)
	RecallSession               // remembered only this session (an Always choice is capped to Once)
	RecallNever                 // never remembered — the grant cache is skipped and it asks every time
)

func (s Scope) String() string {
	switch s {
	case Once:
		return "once"
	case Always:
		return "always"
	default:
		return "unknown"
	}
}
