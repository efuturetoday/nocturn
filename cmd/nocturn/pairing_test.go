package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/device"
)

// postJSON posts body to the test server and returns status + decoded response body.
func postJSON(t *testing.T, url string, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	return resp.StatusCode, v
}

func TestPairingEndpoints(t *testing.T) {
	devices := device.Load("")
	pairings := device.NewPairings(nil)
	mux := http.NewServeMux()
	registerPairing(mux, devices, pairings, nil, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Bootstrap: redeem the OTP → a bearer that the store now recognises.
	pend := pairings.MintBootstrap()
	code, resp := postJSON(t, srv.URL+"/pair", `{"credential":"`+pend.OTP+`","name":"phone"}`)
	if code != http.StatusOK {
		t.Fatalf("/pair status = %d, want 200", code)
	}
	bearer, _ := resp["bearer"].(string)
	if _, ok := devices.Verify(bearer); !ok {
		t.Fatalf("/pair bearer %q not recognised by the store", bearer)
	}

	// A wrong credential is rejected (and there is no live bootstrap left → 401).
	if code, _ := postJSON(t, srv.URL+"/pair", `{"credential":"nope","name":"x"}`); code != http.StatusUnauthorized {
		t.Fatalf("/pair(wrong) status = %d, want 401", code)
	}

	// Wrong method is a 405.
	if r, err := http.Get(srv.URL + "/pair"); err == nil {
		r.Body.Close()
		if r.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET /pair status = %d, want 405", r.StatusCode)
		}
	}

	// Join: the new device gets a joinId but NEVER a code in the response.
	joinStatus, joinResp := postJSON(t, srv.URL+"/join", `{"name":"iPad"}`)
	if joinStatus != http.StatusOK {
		t.Fatalf("/join status = %d, want 200", joinStatus)
	}
	joinID, _ := joinResp["joinId"].(string)
	if joinID == "" {
		t.Fatalf("/join gave no joinId: %v", joinResp)
	}
	if _, leaked := joinResp["code"]; leaked {
		t.Fatalf("/join response leaked a code: %v", joinResp)
	}

	// The code is revealed to already-paired devices over the sync hub (OpenJoins), never to the
	// joining device. Read it the way the hub would.
	var joinCode string
	for _, p := range pairings.OpenJoins() {
		if p.ID == joinID {
			joinCode = p.Code
		}
	}
	if joinCode == "" {
		t.Fatal("OpenJoins did not reveal the join code")
	}

	// Confirming with that code mints the iPad's own bearer.
	confStatus, confResp := postJSON(t, srv.URL+"/join/confirm", `{"joinId":"`+joinID+`","code":"`+joinCode+`"}`)
	if confStatus != http.StatusOK {
		t.Fatalf("/join/confirm status = %d, want 200", confStatus)
	}
	if b, _ := confResp["bearer"].(string); b == "" {
		t.Fatalf("/join/confirm gave no bearer: %v", confResp)
	}
}

// TestRegisterEndpoint proves a paired device can register its push token (bearer-gated), that
// this makes the daemon out-of-band-capable, and that an empty token / bad bearer are handled.
func TestRegisterEndpoint(t *testing.T) {
	devices := device.Load("")
	pairings := device.NewPairings(nil)
	mux := http.NewServeMux()
	registerPairing(mux, devices, pairings, nil, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Pair a device to get a bearer.
	pend := pairings.MintBootstrap()
	_, resp := postJSON(t, srv.URL+"/pair", `{"credential":"`+pend.OTP+`","name":"phone","platform":"ios"}`)
	bearer, _ := resp["bearer"].(string)

	post := func(bearer, body string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/register", strings.NewReader(body))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /register: %v", err)
		}
		r.Body.Close()
		return r.StatusCode
	}

	if code := post("", `{"token":"apns-1"}`); code != http.StatusUnauthorized {
		t.Fatalf("/register without bearer = %d, want 401", code)
	}
	if devices.CanOOB() {
		t.Fatal("CanOOB before any successful register = true")
	}
	if code := post(bearer, `{"token":"apns-1","platform":"ios"}`); code != http.StatusNoContent {
		t.Fatalf("/register with bearer = %d, want 204", code)
	}
	if !devices.CanOOB() {
		t.Fatal("CanOOB after register = false, want true")
	}
	// An empty token clears it (the user revoked push).
	if code := post(bearer, `{"token":""}`); code != http.StatusNoContent {
		t.Fatalf("/register clear = %d, want 204", code)
	}
	if devices.CanOOB() {
		t.Fatal("CanOOB after clearing = true, want false")
	}
}

func TestPairingCORSPreflight(t *testing.T) {
	mux := http.NewServeMux()
	registerPairing(mux, device.Load(""), device.NewPairings(nil), nil, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/pair", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /pair: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("missing Access-Control-Allow-Origin: %v", resp.Header)
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight does not allow Authorization header: %v", resp.Header)
	}
}

func TestResolvePlatform(t *testing.T) {
	req := func(ua string) *http.Request {
		r, _ := http.NewRequest(http.MethodPost, "http://x/pair", nil)
		r.Header.Set("User-Agent", ua)
		return r
	}
	for _, tc := range []struct {
		name     string
		declared string
		ua       string
		want     string
	}{
		{"explicit ios wins over ua", "ios", "Android", "ios"},
		{"explicit android", "Android", "", "android"},
		{"explicit web", "web", "", "web"},
		{"ua iphone", "", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0)", "ios"},
		{"ua android", "", "Mozilla/5.0 (Linux; Android 14)", "android"},
		{"ua macintosh → web", "", "Mozilla/5.0 (Macintosh; Intel Mac OS X)", "web"},
		{"nothing → web", "", "", "web"},
		{"unknown declared falls to ua", "windows", "iPad; CPU OS 17", "ios"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvePlatform(tc.declared, req(tc.ua)); got != tc.want {
				t.Fatalf("resolvePlatform(%q, ua=%q) = %q, want %q", tc.declared, tc.ua, got, tc.want)
			}
		})
	}
}

func TestBearerFrom(t *testing.T) {
	header, _ := http.NewRequest(http.MethodGet, "http://x/ws", nil)
	header.Header.Set("Authorization", "Bearer abc123")
	if got := bearerFrom(header); got != "abc123" {
		t.Fatalf("bearerFrom(header) = %q, want abc123", got)
	}
	query, _ := http.NewRequest(http.MethodGet, "http://x/ws?token=def456", nil)
	if got := bearerFrom(query); got != "def456" {
		t.Fatalf("bearerFrom(query) = %q, want def456", got)
	}
	none, _ := http.NewRequest(http.MethodGet, "http://x/ws", nil)
	if got := bearerFrom(none); got != "" {
		t.Fatalf("bearerFrom(none) = %q, want empty", got)
	}
}
