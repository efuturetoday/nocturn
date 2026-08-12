package serve

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// maxPendingJoins is how many unconfirmed join requests the daemon will hold at once.
//
// Generous for the real case — a household adds devices one at a time, and the honest maximum is
// "however many people are standing here right now" — and low enough that an unauthenticated endpoint
// cannot turn the pairing screen into a wall of noise.
const maxPendingJoins = 8

// handlePair redeems the bootstrap code for the FIRST device's bearer.
func handlePair(w http.ResponseWriter, r *http.Request, devices *auth.Store, log *slog.Logger) {
	var req struct {
		Credential string `json:"credential"`
		Name       string `json:"name"`
		Platform   string `json:"platform"`
	}
	if !decode(w, r, &req) {
		return
	}
	class, ok := classFor(req.Platform)
	if !ok {
		log.Warn("pair refused: unrecognised platform", "platform", req.Platform, "remote", r.RemoteAddr)
		http.Error(w, "unrecognised platform", http.StatusBadRequest)
		return
	}
	bearer, err := devices.Pair(req.Credential, req.Name, req.Platform, class)
	if err != nil {
		pairError(w, r, log, "pair rejected", req.Name, err)
		return
	}
	log.Info("device paired", "name", req.Name)
	writeJSON(w, map[string]string{"bearer": bearer})
}

// handleJoin records a second device's request, returning a joinId — never the code. The code is
// revealed only to a device that may enrol (join.list). It then pushes the refreshed join list to
// every such device that is connected, so an admin device shows the request live instead of only on
// its next connect.
func handleJoin(w http.ResponseWriter, r *http.Request, devices *auth.Store, hub *hub, log *slog.Logger) {
	var req struct {
		Name     string `json:"name"`
		Platform string `json:"platform"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Refuse here rather than at confirm time. The platform arrives on this leg, so this is where an
	// unrecognised one can still be reported to the device that sent it; failing at /join/confirm would
	// strand a human who had already relayed a code.
	if _, ok := classFor(req.Platform); !ok {
		log.Warn("join refused: unrecognised platform", "platform", req.Platform, "remote", r.RemoteAddr)
		http.Error(w, "unrecognised platform", http.StatusBadRequest)
		return
	}
	// /join is unauthenticated by necessity — the caller has nothing to authenticate with yet — so
	// without a ceiling anyone reachable can mint pending joins without limit and bury the household's
	// pairing screen under them. Evicting the oldest keeps the flow working for the person actually
	// standing there: the join they just started is the one that survives.
	devices.CapJoins(maxPendingJoins)

	id := devices.Join(req.Name, req.Platform)

	// How many devices can actually SEE the code. The join list is gated on `enrol`, so a household
	// whose only enrol-capable device is asleep has nowhere to display it — and a client that was told
	// only "here is your joinId" would sit on a code field forever with no error and no hint. Report
	// the number and let the client say something true.
	reachable := hub.countMatching(func(c *conn) bool { return c.can.enrol })
	log.Info("join requested", "name", req.Name, "reachable", reachable)
	writeJSON(w, map[string]any{"joinId": id, "reachable": reachable})

	// Only to connections that may enrol: the list carries the codes, so the push has to be gated
	// exactly as the join.list request is.
	hub.broadcastTo(func(c *conn) bool { return c.can.enrol },
		JoinListResult{Type: "join.list", Joins: devices.PendingJoins()})
}

// handleJoinConfirm redeems a joinId and its relayed code for a new device's bearer.
func handleJoinConfirm(w http.ResponseWriter, r *http.Request, devices *auth.Store, log *slog.Logger) {
	var req struct {
		JoinID string `json:"joinId"`
		Code   string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	// The class comes from the platform recorded when the device ASKED to join, not from anything in
	// this request: the confirm body is the leg a human relays a code through, and re-stating what you
	// are on the leg that grants the bearer would be the one place a holder could name its own class.
	//
	// An unrecognised platform was already refused at /join, so !ok here means the join is gone —
	// expired, spent, or never existed. ConfirmJoin reports that as ErrPairing, which is the answer a
	// caller should get either way, so hand it a class it will never store and let it say so.
	class, _ := classFor(devices.JoinPlatform(req.JoinID))
	bearer, err := devices.ConfirmJoin(req.JoinID, req.Code, class)
	if err != nil {
		pairError(w, r, log, "join confirm rejected", "", err)
		return
	}
	log.Info("device joined")
	writeJSON(w, map[string]string{"bearer": bearer})
}

// handleEnrol brings a new device into the household, on behalf of the device that asks.
//
// Some devices cannot pair themselves. An appliance has no display for a code and no keyboard for
// one, so the join flow — where a device asks and an already-paired device confirms — has nothing to
// work with. Letting it enrol itself is not the alternative: anything that can enrol itself is not
// being authorised by anyone. So an already-paired device asks on its behalf, the human authorises
// where authorising is possible, and the new device only receives what it is handed.
//
// The rule is a subset test, not a list of special cases: a device may only enrol a class whose
// capabilities its own class already covers. An appliance can enrol nothing, so a stolen appliance
// bearer cannot multiply into a household of equally trusted microphones; and no device can hand out
// more authority than it holds.
func handleEnrol(w http.ResponseWriter, r *http.Request, devices *auth.Store, log *slog.Logger) {
	caller, ok := devices.Lookup(bearerOf(r))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Name  string     `json:"name"`
		Class auth.Class `json:"class"`
	}
	if !decode(w, r, &req) {
		return
	}

	mine := capabilitiesOf(caller.Class)
	if !mine.enrol || !mine.covers(capabilitiesOf(req.Class)) {
		log.Warn("enrolment refused", "remote", r.RemoteAddr,
			"caller", caller.Class, "requested", req.Class)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	bearer, err := devices.Mint(req.Name, req.Class)
	if err != nil {
		log.Error("enrolment failed", "name", req.Name, "err", err)
		http.Error(w, "could not enrol", http.StatusInternalServerError)
		return
	}
	log.Info("device enrolled", "name", req.Name, "class", req.Class, "by", caller.Name)
	// Shown once. From here only the hash exists, so a lost bearer means enrolling again rather than
	// recovering it.
	writeJSON(w, map[string]string{"bearer": bearer})
}

// handleArm mints a fresh bootstrap pairing code on behalf of a caller that may open the door.
//
// It exists because a code that is armed once, at startup, for five minutes is a trap. Miss the
// window — walk away from the terminal, come back after lunch, ssh in the following week — and there
// is no code, no way to ask for one, and the only recovery is restarting the daemon or deleting the
// device registry. Every credential with a TTL needs a way to get another one; this is that way, and
// `nocturn pair` is its only caller.
//
// Gated on a capability, never on a class, and granted only to the local command line — whose bearer
// sits in a 0600 file beside the vault. So the authority to open the household is the authority to
// read its directory, which is the same authority that could read the vault anyway.
func handleArm(w http.ResponseWriter, r *http.Request, devices *auth.Store, log *slog.Logger) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	caller, ok := devices.Lookup(bearerOf(r))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !capabilitiesOf(caller.Class).bootstrap {
		log.Warn("pairing code refused", "remote", r.RemoteAddr, "caller", caller.Class)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	code := devices.ArmBootstrap(bootstrapTTL)
	log.Info("pairing code armed", "by", caller.Name, "validFor", bootstrapTTL)
	// The attempt budget is reported rather than restated, so what the CLI prints cannot drift from
	// what the store enforces.
	writeJSON(w, map[string]any{
		"code":             code,
		"expiresInSeconds": int(bootstrapTTL.Seconds()),
		"attempts":         auth.BootstrapMaxTries,
	})
}

// handleRegister records the calling device's push token (bearer-gated), so it can be woken out of
// band for a background approval. An empty token clears it.
func handleRegister(w http.ResponseWriter, r *http.Request, devices *auth.Store, log *slog.Logger) {
	bearer := bearerOf(r)
	if _, ok := devices.Lookup(bearer); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Token    string `json:"token"`
		Platform string `json:"platform"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := devices.RegisterPush(bearer, req.Token, req.Platform); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		log.Error("register push", "err", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decode reads a POST JSON body into v, writing the HTTP error and returning false on any problem.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

// pairError maps a pairing failure to an HTTP status: ErrPairing is a 401 the caller can retry, any
// other error is a 500.
func pairError(w http.ResponseWriter, r *http.Request, log *slog.Logger, msg, name string, err error) {
	if errors.Is(err, auth.ErrPairing) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		log.Warn(msg, "remote", r.RemoteAddr, "name", name)
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
	log.Error(msg, "err", err)
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// bearerOf extracts the bearer from the Authorization header, or the ?token= query when a header
// cannot be set (a browser WebSocket handshake).
func bearerOf(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}
