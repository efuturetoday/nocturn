package serve

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/efuturetoday/nocturn/internal/auth"
)

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
	bearer, err := devices.Pair(req.Credential, req.Name, req.Platform)
	if err != nil {
		pairError(w, r, log, "pair rejected", req.Name, err)
		return
	}
	log.Info("device paired", "name", req.Name)
	writeJSON(w, map[string]string{"bearer": bearer})
}

// handleJoin records a second device's request, returning a joinId — never the code. The code is
// revealed only to an already-paired device (join.list). It then pushes the refreshed join list to
// every connected paired device, so an admin device shows the request live instead of only on its
// next connect.
func handleJoin(w http.ResponseWriter, r *http.Request, devices *auth.Store, hub *hub, log *slog.Logger) {
	var req struct {
		Name     string `json:"name"`
		Platform string `json:"platform"`
	}
	if !decode(w, r, &req) {
		return
	}
	id := devices.Join(req.Name, req.Platform)
	log.Info("join requested", "name", req.Name)
	writeJSON(w, map[string]string{"joinId": id})
	hub.broadcast(JoinListResult{Type: "join.list", Joins: devices.PendingJoins()})
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
	bearer, err := devices.ConfirmJoin(req.JoinID, req.Code)
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
