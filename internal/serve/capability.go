package serve

import (
	"slices"

	"github.com/efuturetoday/nocturn/internal/auth"
)

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
	approve      bool // answer an out-of-band approval
	enrol        bool // bring further devices into the household
	captureAudio bool // make an appliance record its microphone to disk
	bootstrap    bool // arm a fresh pairing code, opening the door from nothing
}

// covers reports whether c permits everything other does — the subset test behind "you may not
// enrol a device more capable than yourself".
func (c capabilities) covers(other capabilities) bool {
	return (!other.approve || c.approve) &&
		(!other.enrol || c.enrol) &&
		(!other.captureAudio || c.captureAudio) &&
		(!other.bootstrap || c.bootstrap)
}

// capabilitiesOf maps a device class to what it may do.
func capabilitiesOf(class auth.Class) capabilities {
	switch class {
	case auth.ClassApp:
		// Someone identified is holding it, so its answer is that person's answer, and it is the
		// thing a household is administered from.
		return capabilities{approve: true, enrol: true, captureAudio: true}
	case auth.ClassAppliance:
		// Nothing. It has no authenticated input path, so it can neither consent on anyone's behalf
		// nor vouch for a new device — and the approval broker takes the FIRST answer it receives,
		// so one that could approve would outrace the phone it exists to defer to.
		//
		// Not captureAudio either, and that one is worth saying out loud: an appliance asking a
		// second appliance to record a room would be a device in the hallway turning on a microphone
		// in the bedroom, with nobody having asked for it.
		return capabilities{}
	case auth.ClassTool:
		// The local command line. It may start a recording, because enrolling a voice needs one and
		// whoever runs the binary already holds the workspace. It approves nothing: a tool that can
		// switch on a microphone must still not be able to release a gated action.
		//
		// It alone may arm a pairing code, and that is the whole of `nocturn pair`. Its bearer lives in
		// a 0600 file beside the vault, so holding it already means holding the workspace — opening a
		// door to a house you are standing inside adds no authority, it only makes the door openable
		// again at 3am six months later instead of only in the first five minutes after a restart.
		//
		// Deliberately NOT given to app or web: a device that can already relay a join code has a way
		// to bring another one in, and handing it this as well would let a phone mint disk-equivalent
		// authority through POST /devices — because covers() would then say an app covers a tool.
		return capabilities{captureAudio: true, bootstrap: true}
	case auth.ClassWeb:
		// A browser holding the page this daemon served. It got here the same way the app did — a
		// bootstrap code printed on the daemon's own stderr, or a join code relayed by an
		// already-paired device — so there is a person at it and its answer is that person's answer.
		//
		// Not captureAudio, and that is the whole difference from ClassApp. Turning on an appliance's
		// microphone from a browser tab is a longer reach than a session in localStorage earns, and
		// nothing the browser UI does needs it. It also keeps covers() honest: a web device does not
		// cover ClassTool, so it cannot mint one.
		return capabilities{approve: true, enrol: true}
	default:
		// Including ClassUnknown: an unrecognised class is not a reason to guess generously.
		return capabilities{}
	}
}

// householdCanEnrol reports whether anything already registered could bring a further device in by
// itself.
//
// It is the answer to "does this household still need a pairing code?", and it lives here because
// that is a question about what a class may DO. Two callers ask it — serveOn, deciding whether to arm
// a code at startup, and handleDaemon, telling a client which way in to offer — and they MUST agree:
// a daemon that armed no code while its clients showed a code field, or the reverse, is exactly the
// dead end this whole shape exists to remove. One function, so they cannot drift.
//
// The test is a capability rather than "is the registry empty", and the difference is the flow: the
// daemon enrols its own command line (ClassTool) at startup, and an appliance may be enrolled on
// someone's behalf. Neither can relay a join code, so counting them as "a device is paired" would
// retire the only code that could pair a first phone — a household nobody can ever enter.
func householdCanEnrol(devices *auth.Store) bool {
	return slices.ContainsFunc(devices.Classes(), func(c auth.Class) bool { return capabilitiesOf(c).enrol })
}

// classFor maps how a holder presents itself — the platform it already sends for push routing — to
// the class it is enrolled as.
//
// It lives beside capabilitiesOf on purpose. That one owns what a class MEANS; this one owns which
// class a holder GETS, and keeping them adjacent is what makes adding a class two cases in one file
// instead of a search. In particular the pairing handlers call this and store the answer: they never
// branch on a class themselves, and the class is never a field the caller can set.
//
// platform is client-supplied and therefore not a fact, which is fine because it is not load-bearing.
// The two classes reachable from here both already require redeeming a bootstrap code printed on the
// daemon's own stderr or a join code relayed by an existing device, so lying about the platform buys
// captureAudio at most. ClassTool and ClassAppliance are not reachable from here at all — those go
// through POST /devices, where covers() is the control.
//
// A platform nobody has taught this function about is reported as unrecognised rather than mapped to
// ClassUnknown and stored. Both are fail-closed — capabilitiesOf answers ClassUnknown with
// capabilities{} — but only one is legible: enrolling the unrecognised holder anyway would hand back
// a bearer that pairs successfully and then silently cannot do anything, which reads as a broken
// daemon. The caller refuses the pairing instead, and it branches on ok, never on the class.
func classFor(platform string) (auth.Class, bool) {
	switch platform {
	case "ios", "android":
		return auth.ClassApp, true
	case "web":
		return auth.ClassWeb, true
	default:
		return auth.ClassUnknown, false
	}
}
