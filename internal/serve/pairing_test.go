package serve

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/auth"
)

func testStore(t *testing.T) *auth.Store {
	t.Helper()
	s, err := auth.New(t.TempDir() + "/devices.json")
	if err != nil {
		t.Fatalf("device store: %v", err)
	}
	return s
}

func post(t *testing.T, h func(http.ResponseWriter, *http.Request), path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw))))
	return rec
}

// The class is derived from the platform the client already sends, never named by the client. A
// browser therefore becomes ClassWeb without having asked for anything, and a phone stays ClassApp.
func TestHandlePair_DerivesTheClassFromThePlatform(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.DiscardHandler)

	for platform, want := range map[string]auth.Class{"web": auth.ClassWeb, "ios": auth.ClassApp} {
		s := testStore(t)
		code := s.ArmBootstrap(time.Minute)
		rec := post(t, func(w http.ResponseWriter, r *http.Request) { handlePair(w, r, s, log) }, "/pair",
			map[string]string{"credential": code, "name": "d", "platform": platform})
		if rec.Code != http.StatusOK {
			t.Fatalf("platform %q: pair = %d (%s)", platform, rec.Code, rec.Body)
		}
		if got := s.Classes(); len(got) != 1 || got[0] != want {
			t.Errorf("platform %q enrolled as %v, want [%s]", platform, got, want)
		}
	}
}

// A class field in the body must change nothing. Letting a holder name its own privilege — even from
// an allowlist — would make a value the client controls into a fact about the client.
func TestHandlePair_IgnoresAClassInTheBody(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	code := s.ArmBootstrap(time.Minute)

	rec := post(t, func(w http.ResponseWriter, r *http.Request) { handlePair(w, r, s, slog.New(slog.DiscardHandler)) },
		"/pair", map[string]string{"credential": code, "name": "d", "platform": "web", "class": "app"})
	if rec.Code != http.StatusOK {
		t.Fatalf("pair = %d (%s)", rec.Code, rec.Body)
	}
	if got := s.Classes(); len(got) != 1 || got[0] != auth.ClassWeb {
		t.Errorf(`a body claiming class "app" enrolled as %v, want [web] — the platform decides`, got)
	}
}

// Refusing beats enrolling something that can do nothing: both are fail-closed, but only one tells
// the operator why the device it just paired is inert.
func TestHandlePair_RefusesAnUnrecognisedPlatform(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"", "windows", "tool"} {
		s := testStore(t)
		code := s.ArmBootstrap(time.Minute)
		rec := post(t, func(w http.ResponseWriter, r *http.Request) { handlePair(w, r, s, slog.New(slog.DiscardHandler)) },
			"/pair", map[string]string{"credential": code, "name": "d", "platform": platform})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("platform %q: pair = %d, want 400", platform, rec.Code)
		}
		if len(s.Classes()) != 0 {
			t.Errorf("platform %q enrolled a device anyway: %v", platform, s.Classes())
		}
	}
}

// The class comes from the platform recorded when the device ASKED to join, not from the confirm
// body — that leg carries only a relayed code, and re-stating what you are on the leg that hands out
// the bearer is exactly where a holder could name its own class.
func TestHandleJoinConfirm_UsesThePlatformFromTheJoinLeg(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	log := slog.New(slog.DiscardHandler)

	hub := newHub(defaultHeartbeat)
	rec := post(t, func(w http.ResponseWriter, r *http.Request) { handleJoin(w, r, s, hub, log) }, "/join",
		map[string]string{"name": "laptop", "platform": "web"})
	if rec.Code != http.StatusOK {
		t.Fatalf("join = %d (%s)", rec.Code, rec.Body)
	}
	var joined struct {
		JoinID string `json:"joinId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &joined); err != nil {
		t.Fatalf("join body: %v", err)
	}

	var code string
	for _, pj := range s.PendingJoins() {
		if pj.JoinID == joined.JoinID {
			code = pj.Code
		}
	}
	if code == "" {
		t.Fatal("no pending code for the join we just made")
	}

	rec = post(t, func(w http.ResponseWriter, r *http.Request) { handleJoinConfirm(w, r, s, log) }, "/join/confirm",
		map[string]string{"joinId": joined.JoinID, "code": code, "platform": "ios"})
	if rec.Code != http.StatusOK {
		t.Fatalf("join confirm = %d (%s)", rec.Code, rec.Body)
	}
	if got := s.Classes(); len(got) != 1 || got[0] != auth.ClassWeb {
		t.Errorf(`confirmed as %v, want [web] — the confirm body said "ios" and must not be heard`, got)
	}
}

func TestHandleJoin_RefusesAnUnrecognisedPlatform(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	hub := newHub(defaultHeartbeat)

	rec := post(t, func(w http.ResponseWriter, r *http.Request) { handleJoin(w, r, s, hub, slog.New(slog.DiscardHandler)) },
		"/join", map[string]string{"name": "mystery", "platform": "beos"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("join = %d, want 400", rec.Code)
	}
	if len(s.PendingJoins()) != 0 {
		t.Error("a join was recorded for a platform that can never be enrolled")
	}
}

// The escape hatch. A code armed once for five minutes at startup is a trap — miss it and the only
// recovery used to be restarting the daemon or deleting the registry, which unpairs everything else.
func TestHandleArm_TheLocalToolCanReopenTheDoorAtAnyTime(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	log := slog.New(slog.DiscardHandler)

	cli, err := s.Mint("cli", auth.ClassTool)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if s.BootstrapPending() {
		t.Fatal("a code was pending before anything armed one")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pair/code", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+cli)
	handleArm(rec, req, s, log)

	if rec.Code != http.StatusOK {
		t.Fatalf("arm = %d (%s)", rec.Code, rec.Body)
	}
	var out struct {
		Code             string `json:"code"`
		ExpiresInSeconds int    `json:"expiresInSeconds"`
		Attempts         int    `json:"attempts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(out.Code) != 6 || out.ExpiresInSeconds <= 0 {
		t.Errorf("armed %+v, want a 6-digit code with a lifetime", out)
	}
	// Reported rather than restated, so what the CLI prints cannot drift from what the store enforces.
	if out.Attempts != auth.BootstrapMaxTries {
		t.Errorf("attempts = %d, want %d", out.Attempts, auth.BootstrapMaxTries)
	}
	if !s.BootstrapPending() {
		t.Error("the store does not report the code it just armed")
	}
	// And it actually pairs — the whole point is a door that opens, not a code that prints.
	if _, err := s.Pair(out.Code, "browser", "web", auth.ClassWeb); err != nil {
		t.Errorf("the armed code did not pair: %v", err)
	}
}

func TestHandleArm_RefusedWithoutTheCapability(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.DiscardHandler)

	for _, class := range []auth.Class{auth.ClassApp, auth.ClassWeb, auth.ClassAppliance} {
		s := testStore(t)
		bearer, err := s.Mint("holder", class)
		if err != nil {
			t.Fatalf("mint %s: %v", class, err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/pair/code", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+bearer)
		handleArm(rec, req, s, log)

		if rec.Code != http.StatusForbidden {
			t.Errorf("%s armed a code: %d", class, rec.Code)
		}
		if s.BootstrapPending() {
			t.Errorf("%s armed a code despite the refusal", class)
		}
	}

	// No bearer at all is 401, not 403: nothing was identified, so there is nothing to refuse.
	s := testStore(t)
	rec := httptest.NewRecorder()
	handleArm(rec, httptest.NewRequest(http.MethodPost, "/pair/code", strings.NewReader("{}")), s, log)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated arm = %d, want 401", rec.Code)
	}
}

// daemon.json is what a client reads before it can possibly hold a bearer, so it must say enough to
// connect and nothing about authority.
func TestHandleDaemon_SaysWhereToConnectAndNothingAboutAuthority(t *testing.T) {
	t.Parallel()
	s := testStore(t)

	rec := httptest.NewRecorder()
	handleDaemon(rec, httptest.NewRequest(http.MethodGet, "/daemon.json", nil), s, "1.2.3")
	if rec.Code != http.StatusOK {
		t.Fatalf("daemon.json = %d", rec.Code)
	}

	var got Daemon
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body: %v", err)
	}
	if got.WS != "/ws" || got.Version != "1.2.3" || got.Name == "" {
		t.Errorf("descriptor = %+v, want a name, version 1.2.3 and ws /ws", got)
	}
	if got.Bootstrap {
		t.Error("bootstrap reported armed on a store where nothing armed it")
	}

	// The one live field: it follows the code, and goes false again once the code is spent.
	code := s.ArmBootstrap(time.Minute)
	rec = httptest.NewRecorder()
	handleDaemon(rec, httptest.NewRequest(http.MethodGet, "/daemon.json", nil), s, "1.2.3")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body: %v", err)
	}
	if !got.Bootstrap {
		t.Error("bootstrap reported unarmed while a code is pending")
	}

	if _, err := s.Pair(code, "phone", "ios", auth.ClassApp); err != nil {
		t.Fatalf("pair: %v", err)
	}
	rec = httptest.NewRecorder()
	handleDaemon(rec, httptest.NewRequest(http.MethodGet, "/daemon.json", nil), s, "1.2.3")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body: %v", err)
	}
	if got.Bootstrap {
		t.Error("bootstrap still reported armed after the code was redeemed")
	}

	// Nothing in the descriptor may name a class or a capability: a caller with no bearer is not a
	// holder yet, and the shape of the household is not theirs to read.
	body := rec.Body.String()
	for _, leak := range []string{"class", "capab", "approve", "enrol", "device"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("daemon.json mentions %q: %s", leak, body)
		}
	}
}
