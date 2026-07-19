package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/efuturetoday/nocturn/internal/device"
)

// registerPairing wires the device-pairing HTTP endpoints onto the mux. The pairing handshakes
// are bearer-LESS (a new device has no bearer yet); /register is bearer-gated. All auth/pairing
// logic lives here in cmd, so appserver stays pure state. onJoinChange is fired when a join is
// minted, so the caller can push the pending-join list (with codes) to already-paired devices
// over the sync hub (revealing the code only to authed connections).
//
//	POST /pair          {credential,name,platform} → {bearer}   bootstrap QR/OTP, first device
//	POST /join          {name,platform}            → {joinId}    a new device asks to join (no code)
//	POST /join/confirm  {joinId,code}              → {bearer}    the code, typed off a trusted screen
//	POST /register      {token,platform} (bearer)  → 204         the app's push token (OOB reach)
func registerPairing(mux *http.ServeMux, devices *device.Store, pairings *device.Pairings, onJoinChange func()) {
	// The app connects from a foreign origin (a Capacitor webview / dev server), so the browser
	// sends a CORS preflight OPTIONS before each request. handle wraps every endpoint to answer
	// the preflight and set the CORS headers — consistent with the WS's InsecureSkipVerify
	// posture: the bearer, not the Origin, is the trust boundary.
	handle := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, withCORS(h)) }

	handle("/pair", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Credential, Name, Platform string }
		if !decodeJSON(w, r, &req) {
			return
		}
		bearer, err := pairings.RedeemBootstrap(req.Credential, deviceName(req.Name), resolvePlatform(req.Platform, r), devices)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]string{"bearer": bearer})
	})

	handle("/join", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name, Platform string }
		if !decodeJSON(w, r, &req) {
			return
		}
		// The code is NOT returned to the requester — only the joinId. It reaches the human via
		// the sync-hub push to already-paired devices (onJoinChange).
		joinID := pairings.MintJoin(deviceName(req.Name), resolvePlatform(req.Platform, r)).ID
		if onJoinChange != nil {
			onJoinChange()
		}
		writeJSON(w, map[string]string{"joinId": joinID})
	})

	handle("/join/confirm", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ JoinID, Code string }
		if !decodeJSON(w, r, &req) {
			return
		}
		bearer, err := pairings.RedeemJoin(req.JoinID, req.Code, devices)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]string{"bearer": bearer})
	})

	handle("/register", func(w http.ResponseWriter, r *http.Request) {
		dev, ok := devices.Verify(bearerFrom(r))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct{ Token, Platform string }
		if !decodeJSON(w, r, &req) {
			return
		}
		// An empty token clears the registration (the user revoked push). Success is 204.
		devices.SetPushToken(dev.ID, req.Token, resolvePlatform(req.Platform, r))
		w.WriteHeader(http.StatusNoContent)
	})
}

// printBootstrap renders the first-run pairing to stdout: a scannable QR carrying the daemon's
// LAN address plus the pending secret, and the typed OTP fallback beneath it.
func printBootstrap(addr string, p device.Pending) {
	_, port, _ := net.SplitHostPort(addr)
	host := outboundIP()
	uri := fmt.Sprintf("nocturn://pair?host=%s&port=%s&secret=%s", host, port, p.Secret)

	fmt.Println("\n── Pair a device ─────────────────────────────────")
	if img, err := renderQR(uri); err == nil {
		fmt.Print(img)
	}
	fmt.Printf("\nScan the QR, or enter this code in the app:  %s\n", p.OTP)
	if host != "" {
		fmt.Printf("Daemon address: %s:%s\n", host, port)
	}
	fmt.Println("──────────────────────────────────────────────────")
}

// withCORS answers the browser's CORS preflight and stamps the CORS headers on every response,
// so the companion app (a foreign origin) can fetch the pairing endpoints. A preflight OPTIONS is
// short-circuited with 204 before the wrapped handler's method check. Allow-Origin is "*" because
// auth rides an explicit bearer header, not cookies — there is no ambient credential to protect.
func withCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "600") // cache the preflight; avoid re-OPTIONS per request
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h(w, r)
	}
}

// bearerFrom extracts the caller's bearer from the Authorization header ("Bearer <token>"), or
// the ?token= query as a fallback for clients that cannot set a header on a WebSocket handshake.
func bearerFrom(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.URL.Query().Get("token")
}

// resolvePlatform normalizes the client-declared platform, falling back to the User-Agent when
// the app sent none, so a device's OS is ALWAYS recorded (it routes the push provider:
// ios→APNs, android→FCM). An explicit, known value always wins; otherwise a best-effort guess
// from the UA, defaulting to "web" (a browser has no native push token anyway).
func resolvePlatform(declared string, r *http.Request) string {
	switch strings.ToLower(strings.TrimSpace(declared)) {
	case "ios":
		return "ios"
	case "android":
		return "android"
	case "web":
		return "web"
	}
	ua := strings.ToLower(r.UserAgent())
	switch {
	case strings.Contains(ua, "iphone"), strings.Contains(ua, "ipad"), strings.Contains(ua, "ipod"):
		return "ios"
	case strings.Contains(ua, "android"):
		return "android"
	default:
		return "web"
	}
}

// deviceName trims the client-supplied name, falling back to a generic label so a device always
// has something to show in the paired list.
func deviceName(name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return "device"
}

// outboundIP reports the LAN IP the daemon would use to reach the internet — the address to put
// in the pairing QR so the phone connects to the right interface. A UDP "dial" only selects a
// route (no packet is sent); an error yields "" (the QR then omits the host, mDNS still works).
func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// writeJSON encodes v as a JSON response. Encoding a plain map/struct cannot fail meaningfully,
// so the error is dropped (the header is already committed).
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON enforces POST and decodes the JSON body into v, writing the right 4xx and returning
// false on a wrong method or malformed body.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return false
	}
	return true
}
