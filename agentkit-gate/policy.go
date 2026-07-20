package gate

// Policy decides, from the action alone, whether it is freely allowed, must be asked, or denied. It
// is the static, coarse layer; the grant + approval layer sits on top of an Ask.
type Policy interface {
	Decide(a Action) Decision
}

// PolicyFunc adapts a plain function to Policy.
type PolicyFunc func(Action) Decision

func (f PolicyFunc) Decide(a Action) Decision { return f(a) }

// Classify builds a Policy from Kind lists: a Kind in denied is always Deny, a Kind in guarded is
// always Ask, and every other Kind is Allow. Kinds are tool names and/or shared axes (e.g. "net").
// The common default: reads Allow, writes/sends Ask, exec Deny.
func Classify(guarded, denied []string) Policy {
	guardedSet := toSet(guarded)
	deniedSet := toSet(denied)
	return PolicyFunc(func(a Action) Decision {
		switch {
		case deniedSet[a.Kind]:
			return Deny
		case guardedSet[a.Kind]:
			return Ask
		default:
			return Allow
		}
	})
}

func toSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}
