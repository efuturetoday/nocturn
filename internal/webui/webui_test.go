package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/webui"
)

// Handler must be usable whether or not anyone ran generate.sh — a bare clone builds and runs, it
// simply has no UI in it. This is the contract the daemon relies on: it wires the handler
// unconditionally and never asks whether there is a bundle behind it.
func TestHandler_AlwaysServesSomething(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if webui.Available() {
		if rec.Code != http.StatusOK {
			t.Fatalf("with a bundle built in, GET / = %d, want 200", rec.Code)
		}
		return
	}

	// 503 rather than 404: the route exists and the daemon is healthy, the assets are just not here.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a bundle, GET / = %d, want 503", rec.Code)
	}
	// The page has to say which command fixes it. A blank tab is how "I built it and nothing
	// happened" turns into an afternoon.
	if body := rec.Body.String(); !strings.Contains(body, "npm run build") {
		t.Errorf("the stub page does not say how to build the UI:\n%s", body)
	}
}

// Whatever the bundle state, every path answers — the SPA fallback and the stub both cover
// everything, so there is no path that 404s and no path that panics.
func TestHandler_EveryPathAnswers(t *testing.T) {
	t.Parallel()
	h := webui.Handler()
	want := http.StatusOK
	if !webui.Available() {
		want = http.StatusServiceUnavailable
	}
	for _, path := range []string{"/", "/index.html", "/app/chat/abc", "/nope/../nope"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}
