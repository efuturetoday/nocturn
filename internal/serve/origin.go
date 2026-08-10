package serve

import (
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
)

// Who is allowed to talk to this daemon from inside a browser.
//
// This exists because "on the LAN" and "reachable from a browser" are not the same set, and the
// difference is not small. A page on any website the household visits can issue cross-origin requests
// to 192.168.x.x — the attacker's code runs in a browser that IS on the network, so the network
// boundary buys nothing. Nothing here carries cookies, so the usual reason allow-all is harmless does
// not apply either: with Access-Control-Allow-Origin: * the caller READS the response, and the
// response to /pair is a bearer.
//
// The rule is deliberately about the browser and only the browser. A request with NO Origin header —
// curl, the CLI's own WebSocket dial, any native client — is not a cross-origin browser request and is
// not what this defends against; it is subject to the bearer gate like everything else. Refusing those
// would break every non-browser client to stop an attack none of them can carry.
//
// Applied once, in cors, which wraps the whole mux — so it covers the pairing endpoints and the /ws
// upgrade from a single place, and there is no second list to drift out of step.

// nativeOrigins are the app's own webview origins. Both are Capacitor's DEFAULTS for the pinned
// version, verified in its source rather than remembered: iOS builds its origin from
// InstanceDescriptorDefaults (scheme "capacitor", hostname "localhost"), Android from
// CapConfig.CAPACITOR_HTTPS_SCHEME ("https") with the same hostname. mobile/capacitor.config.ts
// overrides neither. If either is ever set there, this list has to move with it — getting it wrong
// locks the phone out of its own daemon.
var nativeOrigins = []string{
	"capacitor://localhost", // iOS
	"https://localhost",     // Android
	"ionic://localhost",     // older Capacitor/Ionic builds still in the wild
}

// devOriginsEnv names extra origins to accept, comma-separated.
//
// It exists for one real workflow: `ng serve` on port 4200 against a daemon on another host
// (mobile/README.md). That page's origin carries whatever address the developer typed, so it cannot
// be known here — and guessing it, by accepting any origin on port 4200 say, would widen the hole for
// everyone to spare one person one environment variable.
const devOriginsEnv = "NOCTURN_DEV_ORIGINS"

// hostOK reports whether the address a request was sent TO is one a stranger cannot point at this
// daemon.
//
// It exists because the Origin check below cannot stand alone. That check asks "did this page come
// from the same place it is talking to", by comparing Origin against Host — and both of those are
// derived from the URL the browser was pointed at. An attacker who owns a DNS name owns both.
//
// That is DNS rebinding, and it is the standard way a page on the open web reaches a service on
// someone's home network: serve evil.example with a one-second TTL, let the victim load it, then
// repoint the name at 192.168.1.20. The browser now considers the page same-origin with the daemon,
// sends Origin: http://evil.example and Host: evil.example, and a check that compares the two agrees.
// Measured against this daemon before this function existed: refused with a foreign Host, ACCEPTED
// once Origin and Host matched.
//
// The defence is to require a Host that cannot be rebound. An IP literal cannot: the browser resolves
// nothing, so there is no answer for an attacker to change. `localhost` cannot. A `.local` name is
// answered by mDNS on the local segment, so claiming one already requires being on the network. A
// plain DNS name is exactly the case that cannot be distinguished from an attack, so it has to be
// named on purpose.
func hostOK(host string) bool {
	h := host
	if only, _, err := net.SplitHostPort(host); err == nil {
		h = only
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]") // an IPv6 literal arrives bracketed
	switch {
	case h == "":
		return false
	case net.ParseIP(h) != nil:
		return true
	case h == "localhost", strings.HasSuffix(h, ".localhost"):
		return true
	case strings.HasSuffix(h, ".local"):
		return true
	}
	for allowed := range strings.SplitSeq(os.Getenv(hostnamesEnv), ",") {
		if allowed = strings.TrimSpace(allowed); allowed != "" && strings.EqualFold(allowed, h) {
			return true
		}
	}
	return false
}

// hostnamesEnv names the DNS hostnames this daemon may be addressed by, comma-separated.
//
// Empty by default, which is the right default: reaching a daemon by IP, by `localhost` or by its
// mDNS `.local` name needs nothing, and those cover how it is actually reached. Anyone who has put a
// real hostname in front of it has done something deliberate and can say so once.
const hostnamesEnv = "NOCTURN_HOSTNAMES"

// originOK reports whether a request may be served given where the browser making it came from.
func originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a cross-origin browser request at all
	}
	// The page this daemon served, whatever address it was reached on. Compared against r.Host rather
	// than a configured name so it keeps working on every interface, port and hostname the daemon
	// answers to, with nothing to configure.
	if sameOrigin(origin, r.Host) {
		return true
	}
	if slices.Contains(nativeOrigins, origin) {
		return true
	}
	for allowed := range strings.SplitSeq(os.Getenv(devOriginsEnv), ",") {
		if allowed = strings.TrimSpace(allowed); allowed != "" && origin == allowed {
			return true
		}
	}
	return false
}

// sameOrigin reports whether origin addresses host. Compared on the authority alone: the scheme is
// http today and https the day there is TLS, and a page served over one must not be locked out by a
// rule written for the other.
func sameOrigin(origin, host string) bool {
	if host == "" {
		return false
	}
	authority := origin
	if i := strings.Index(authority, "://"); i >= 0 {
		authority = authority[i+3:]
	}
	return authority == host
}
