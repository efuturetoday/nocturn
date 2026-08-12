package serve

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// broadcast never blocks on a congested connection: a conn whose buffer is full drops the message
// (and resyncs later), while others still receive it.
func TestHub_Broadcast_NonBlocking_DropsFullBuffer(t *testing.T) {
	h := newHub(defaultHeartbeat)

	full := &conn{control: make(chan any, 64)}
	for range 64 { // saturate the buffer (newConn's cap)
		full.control <- "filler"
	}
	open := &conn{control: make(chan any, 64)}

	h.add(full)
	h.add(open)

	done := make(chan struct{})
	go func() {
		h.broadcast("live")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("broadcast blocked on a full connection")
	}

	if len(full.control) != 64 {
		t.Errorf("full conn buffer = %d, want 64 (message dropped, not enqueued)", len(full.control))
	}
	select {
	case got := <-open.control:
		if got != "live" {
			t.Errorf("open conn received %v, want live", got)
		}
	default:
		t.Error("open conn did not receive the broadcast")
	}
}

// Concurrent broadcasts with a live reader must be race-clean (run with -race).
func TestHub_Broadcast_Concurrent(t *testing.T) {
	h := newHub(defaultHeartbeat)
	c := &conn{control: make(chan any, 64)}
	h.add(c)

	// Drain continuously so the buffer never wedges.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-c.control:
			}
		}
	}()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				h.broadcast("msg")
			}
		}()
	}
	// Churn membership concurrently to stress the hub's lock.
	wg.Add(1)
	go func() {
		defer wg.Done()
		other := &conn{control: make(chan any, 64)}
		for range 100 {
			h.add(other)
			h.remove(other)
		}
	}()
	wg.Wait()
	close(stop)
}

func TestCors_OptionsPreflight204(t *testing.T) {
	var innerCalled bool
	h := cors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { innerCalled = true }), discardLog())

	rec := httptest.NewRecorder()
	// A realistic Host: httptest defaults it to example.com, which hostOK rightly refuses — a plain
	// DNS name is indistinguishable from a rebinding attack.
	req := httptest.NewRequest(http.MethodOptions, "http://192.168.1.20:8080/pair", nil)
	req.Header.Set("Origin", "capacitor://localhost")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if innerCalled {
		t.Error("preflight must not reach the wrapped handler")
	}
	// The caller's origin, echoed — not "*". The response to /pair is a bearer, so a wildcard would
	// let any page in the world read it.
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "capacitor://localhost" {
		t.Errorf("Allow-Origin = %q, want the caller's origin echoed", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin — the answer depends on it, so a cache must not reuse it", got)
	}
}

func TestCors_PassesThroughNonPreflight(t *testing.T) {
	var innerCalled bool
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	}), discardLog())

	rec := httptest.NewRecorder()
	// No Origin at all: curl, the CLI, any native client. Not a cross-origin browser request, so it
	// is not what the check defends against, and refusing it would break every non-browser caller.
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://192.168.1.20:8080/join", nil))

	if !innerCalled {
		t.Error("non-preflight request must reach the wrapped handler")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want no header at all when there was no Origin", got)
	}
}

// The attack this exists for: a page on any site the household visits can reach 192.168.x.x, and the
// response it would read is a bearer. Being on the LAN is not a property the network can check —
// the attacker's code runs in a browser that already is.
func TestCors_RefusesAStrangersOrigin(t *testing.T) {
	var innerCalled bool
	h := cors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { innerCalled = true }), discardLog())

	for _, origin := range []string{
		"https://evil.example",
		"http://evil.example",
		"null",
		"http://localhost:4200",      // the dev server, unless NOCTURN_DEV_ORIGINS says so
		"http://192.168.1.99:8080",   // a different daemon's address
		"capacitor://localhost.evil", // near-miss on the native origin
	} {
		for _, method := range []string{http.MethodOptions, http.MethodPost} {
			innerCalled = false
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/pair", nil)
			req.Host = "192.168.1.20:8080"
			req.Header.Set("Origin", origin)
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("%s /pair from %q = %d, want 403", method, origin, rec.Code)
			}
			if innerCalled {
				t.Errorf("%s /pair from %q reached the handler — it would spend a pairing attempt", method, origin)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("%s /pair from %q got Allow-Origin %q", method, origin, got)
			}
		}
	}
}

// The page the daemon itself served, at whatever address it was reached on. Compared against r.Host
// so it works on every interface and port with nothing to configure.
func TestCors_AllowsThePageItServed(t *testing.T) {
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), discardLog())

	for host, origin := range map[string]string{
		"192.168.1.20:8080": "http://192.168.1.20:8080",
		"127.0.0.1:8080":    "http://127.0.0.1:8080",
		"nocturn.local:80":  "https://nocturn.local:80", // scheme is not compared: TLS one day
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/pair", nil)
		req.Host = host
		req.Header.Set("Origin", origin)
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("origin %q at host %q = %d, want 200", origin, host, rec.Code)
		}
	}
}

// Both are Capacitor DEFAULTS for the pinned version, read out of its source. Getting either wrong
// locks the shipped phone app out of its own daemon, which is why they are pinned by a test.
func TestCors_AllowsTheAppsOwnWebviews(t *testing.T) {
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), discardLog())

	for _, origin := range []string{"capacitor://localhost", "https://localhost", "ionic://localhost"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/pair", nil)
		req.Host = "192.168.1.20:8080"
		req.Header.Set("Origin", origin)
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("native origin %q = %d, want 200", origin, rec.Code)
		}
	}
}

// `ng serve` against a daemon on another host carries whatever address the developer typed, so it
// cannot be known here. An explicit opt-in beats guessing — accepting any origin on port 4200 would
// widen the hole for everyone to spare one person one variable.
func TestCors_DevOriginsAreOptIn(t *testing.T) {
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), discardLog())
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/pair", nil)
		r.Host = "192.168.1.20:8080"
		r.Header.Set("Origin", "http://192.168.2.179:4200")
		return r
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dev origin without the env var = %d, want 403", rec.Code)
	}

	t.Setenv(devOriginsEnv, "http://example.invalid, http://192.168.2.179:4200")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req())
	if rec.Code != http.StatusOK {
		t.Errorf("dev origin with the env var = %d, want 200", rec.Code)
	}
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// DNS rebinding: an attacker who owns a name owns BOTH the Origin and the Host, so a check that
// compares them to each other agrees with itself. Serve evil.example with a one-second TTL, let the
// victim load it, then repoint the name at the daemon's LAN address — the browser now calls the page
// same-origin with the daemon. Measured against this daemon before hostOK existed: refused with a
// foreign Host, ACCEPTED once Origin and Host matched.
func TestCors_RefusesARebindableHost(t *testing.T) {
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), discardLog())

	for _, host := range []string{"evil.example", "evil.example:8080", "nocturn.attacker.test"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/pair", nil)
		req.Host = host
		req.Header.Set("Origin", "http://"+host) // what the browser sends after the rebind
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q with a matching Origin = %d, want 403", host, rec.Code)
		}
	}
}

// The addresses a daemon is actually reached by, none of which a stranger can point at it: an IP
// literal resolves nothing, and a .local name is answered by mDNS on the local segment.
func TestCors_AllowsAddressesThatCannotBeRebound(t *testing.T) {
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), discardLog())

	for _, host := range []string{
		"127.0.0.1:8080", "192.168.1.20:8080", "[::1]:8080", "localhost:8080", "localhost",
		"nocturn.local:8080",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/pair", nil)
		req.Host = host
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("Host %q = %d, want 200", host, rec.Code)
		}
	}
}

// A real hostname in front of the daemon is a deliberate setup, and it is also exactly what an attack
// looks like — so it is named once rather than guessed.
func TestCors_HostnamesAreOptIn(t *testing.T) {
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), discardLog())
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/pair", nil)
		r.Host = "nocturn.example.org"
		return r
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unnamed hostname = %d, want 403", rec.Code)
	}

	t.Setenv(hostnamesEnv, "other.invalid, Nocturn.Example.ORG")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req())
	if rec.Code != http.StatusOK {
		t.Errorf("named hostname = %d, want 200 (hostnames are case-insensitive)", rec.Code)
	}
}
