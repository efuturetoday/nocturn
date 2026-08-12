package serve

import (
	"net/http"
	"os"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// Daemon is what a client learns before it has a bearer: which daemon this is, where its socket is,
// and which way in to offer.
//
// It exists for the browser. A phone is handed a host by mDNS or by a human typing one, so it always
// knows where it is pointed; a page served BY the daemon knows only its own location, and asking
// "were you served by a nocturn?" is how it decides between offering a host picker and simply
// connecting back to itself.
//
// Deliberately says nothing about authority. No class, no capability, no device list — those are
// answers to "what may this holder do", and a caller with no bearer is not a holder yet.
type Daemon struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	WS      string `json:"ws"`
	// Paired reports that something in the household can already bring a further device in — so the
	// join flow is a real option, with someone on the other end of it to relay a code.
	//
	// It exists because one bit could not tell "join is the way in" from "there is no way in right
	// now", and collapsing those two IS the dead end: a client that assumed the first, when the truth
	// was the second, walked the user into a flow that could never complete and offered no way back.
	// Two bits, four states, four screens, no guessing.
	Paired bool `json:"paired"`
	// Bootstrap reports that a pairing code is armed right now. It reveals nothing that attempting to
	// redeem a code would not — and with a five-guess budget on that code, knowing one exists is not
	// worth anything to an attacker who would have to spend the guesses either way.
	Bootstrap bool `json:"bootstrap"`
}

// handleDaemon answers GET /daemon.json. Unauthenticated on purpose: it is the question a client asks
// before it can possibly have a bearer, and every field is either public (the hostname it just
// connected to) or already inferable (whether pairing is open).
func handleDaemon(w http.ResponseWriter, r *http.Request, devices *auth.Store, version string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, Daemon{
		Name:    daemonName(),
		Version: version,
		WS:      "/ws",
		// The SAME predicate serveOn used to decide whether to arm a code. If these two ever answered
		// differently, a client would offer a code field for a code nobody armed, or offer the join
		// flow with nothing on the other end — which is the failure this field exists to end.
		Paired:    householdCanEnrol(devices),
		Bootstrap: devices.BootstrapPending(),
	})
}

// daemonName is the human label for this daemon — the same form the mDNS instance uses, so a browser
// and the app's host list call one machine by one name.
func daemonName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return "nocturn @ " + h
	}
	return "nocturn"
}
