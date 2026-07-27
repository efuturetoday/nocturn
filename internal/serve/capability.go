package serve

import "github.com/efuturetoday/nocturn/internal/auth"

// capabilities is what a class of device may do, and capabilitiesOf below is the ONE place a class
// is ever interpreted.
//
// The split follows agent.Autonomy: the category is stored where the thing lives (internal/agent
// there, internal/auth here) and the single site that turns it into authority lives where things are
// assembled (internal/workspace there, this package here). Before this, two independent decisions
// each inlined their own "is it an app" comparison — which is how a third capability quietly becomes
// a third comparison, and how adding a class turns into a search.
//
// Every field is a capability the holder may exercise, so the zero value is a device that may do
// nothing. A class nobody has taught this function about therefore lands on the safe side by
// construction rather than by anyone remembering to handle it.
type capabilities struct {
	approve bool // answer an out-of-band approval
	enrol   bool // bring further devices into the household
}

// covers reports whether c permits everything other does — the subset test behind "you may not
// enrol a device more capable than yourself".
func (c capabilities) covers(other capabilities) bool {
	return (!other.approve || c.approve) && (!other.enrol || c.enrol)
}

// capabilitiesOf maps a device class to what it may do.
func capabilitiesOf(class auth.Class) capabilities {
	switch class {
	case auth.ClassApp:
		// Someone identified is holding it, so its answer is that person's answer, and it is the
		// thing a household is administered from.
		return capabilities{approve: true, enrol: true}
	case auth.ClassAppliance:
		// Nothing. It has no authenticated input path, so it can neither consent on anyone's behalf
		// nor vouch for a new device — and the approval broker takes the FIRST answer it receives,
		// so one that could approve would outrace the phone it exists to defer to.
		return capabilities{}
	default:
		// Including ClassUnknown: an unrecognised class is not a reason to guess generously.
		return capabilities{}
	}
}
