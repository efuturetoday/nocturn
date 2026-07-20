package gate

// Ruling is a policy's verdict on an action: the Decision (allow / ask / deny) and, for an Ask, the
// Recall ceiling — how long an approval of this Kind may be remembered.
type Ruling struct {
	Decision Decision
	Recall   Recall
}

// Policy decides, from the action alone, whether it is freely allowed, must be asked, or denied — and
// for an Ask, the Recall ceiling on remembering it. It is the static, coarse layer; the grant +
// approval layer sits on top of an Ask.
type Policy interface {
	Decide(a Action) Ruling
}

// PolicyFunc adapts a plain function to Policy. Use it for per-Kind Recall control, e.g. return
// Ruling{Ask, RecallNever} for an irreversible Kind so it is asked every time.
type PolicyFunc func(Action) Ruling

func (f PolicyFunc) Decide(a Action) Ruling { return f(a) }

// Classify builds a Policy from Kind lists: a Kind in denied is Deny, a Kind in guarded is Ask
// (RecallAlways — may be remembered durably), and every other Kind is Allow. Kinds are tool names
// and/or shared axes (e.g. "net"). For per-Kind Recall limits, write a PolicyFunc instead.
func Classify(guarded, denied []string) Policy {
	guardedSet := toSet(guarded)
	deniedSet := toSet(denied)
	return PolicyFunc(func(a Action) Ruling {
		switch {
		case deniedSet[a.Kind]:
			return Ruling{Decision: Deny}
		case guardedSet[a.Kind]:
			return Ruling{Decision: Ask, Recall: RecallAlways}
		default:
			return Ruling{Decision: Allow}
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
