package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// bundle is a stand-in for what Angular emits: a compiled chunk, an asset copied out of public/, and
// index.html.
func bundle() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><app-root></app-root>")},
		"main-ABC12345.js":      {Data: []byte("console.log(1)")},
		"assets/brand/logo.png": {Data: []byte("\x89PNG")},
	}
}

func TestHandlerFor_ServesRealFiles(t *testing.T) {
	t.Parallel()
	h := handlerFor(bundle())

	// Both are real files and both must be served. What may be cached, and for how long, depends on
	// whether the name is content-addressed — see TestHandlerFor_CachesOnlyHashedNamesForever.
	for path, want := range map[string]string{
		"/main-ABC12345.js":      "console.log(1)",
		"/assets/brand/logo.png": "\x89PNG",
	} {
		rec := do(h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if got := rec.Body.String(); got != want {
			t.Errorf("GET %s served %q, want %q", path, got, want)
		}
	}
}

// The app's routes are history-API paths, so a reload or a shared deep link arrives as a path with
// no file behind it. Answering 404 would break every link the UI hands out.
func TestHandlerFor_FallsBackToIndexForRoutes(t *testing.T) {
	t.Parallel()
	h := handlerFor(bundle())

	// "/index.html" is in the list on purpose: it is a real file, and it must still take the index
	// path — FileServerFS would answer it with a 301 to /, a surprise on the app's own entry point.
	for _, path := range []string{"/", "/index.html", "/app/home", "/app/chat/abc123", "/app/agents/run/x"} {
		rec := do(h, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		if body := rec.Body.String(); body != "<!doctype html><app-root></app-root>" {
			t.Errorf("GET %s served %q, want index.html", path, body)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", path, got)
		}
	}
}

// A directory is not a file to serve. FileServer would answer a listing, which publishes the shape
// of the bundle for no benefit — the SPA never navigates to one.
func TestHandlerFor_DirectoryFallsBackToIndex(t *testing.T) {
	t.Parallel()
	rec := do(handlerFor(bundle()), "/assets/brand")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /assets/brand = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "<!doctype html><app-root></app-root>" {
		t.Errorf("GET /assets/brand served a listing, want index.html: %q", body)
	}
}

// A traversal attempt must not escape the bundle. path.Clean resolves it before the lookup, so the
// worst case is the SPA fallback rather than a file from outside.
func TestHandlerFor_RejectsTraversal(t *testing.T) {
	t.Parallel()
	rec := do(handlerFor(bundle()), "/../../etc/passwd")
	if rec.Code != http.StatusOK {
		t.Fatalf("traversal = %d, want 200 (the index fallback)", rec.Code)
	}
	if body := rec.Body.String(); body != "<!doctype html><app-root></app-root>" {
		t.Errorf("traversal served %q, want index.html", body)
	}
}

// The UI is the catch-all route, so a write to a MISTYPED protocol endpoint lands here. Answering it
// with 200 and a page of HTML is the least useful way to learn the path was wrong.
func TestHandlerFor_RefusesWrites(t *testing.T) {
	t.Parallel()
	h := handlerFor(bundle())

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/regsiter", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /regsiter = %d, want 405", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s Allow = %q, want \"GET, HEAD\"", method, got)
		}
	}

	// HEAD is a read, and a browser preflighting the entry point must not be refused.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/app/home", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD /app/home = %d, want 200", rec.Code)
	}
}

func do(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// One rule for everything, and it is the absence of a rule. Caching by filename meant trusting that
// Angular hashes what it emits — true of the compiled chunks and silently false for anything copied
// out of public/, so favicon.ico and mascot.png were pinned for a year under names that outlive their
// bytes. On a LAN the download it saved was never worth the class of bug it bought.
func TestHandlerFor_NothingIsCached(t *testing.T) {
	t.Parallel()
	h := handlerFor(bundle())

	for _, p := range []string{
		"/",                      // the SPA entry point
		"/index.html",            // the same file by name
		"/main-ABC12345.js",      // a compiled chunk, hashed
		"/assets/brand/logo.png", // copied verbatim, name outlives its bytes
		"/app/chat/abc",          // a client-side route
	} {
		if got := do(h, p).Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", p, got)
		}
	}
}
