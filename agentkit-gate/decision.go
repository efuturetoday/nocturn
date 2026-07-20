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

// Scope is how long an approval holds.
type Scope int

const (
	Once   Scope = iota // this session only
	Always              // remembered durably
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
