// Package webui serves the browser front-end out of the binary itself.
//
// The bundle is the SAME artefact the companion app ships — one Angular build in mobile/, embedded
// here rather than forked. That is what keeps a browser and a phone showing the same product: the
// chat reducer, the approval overlay and the protocol types have exactly one implementation, and a
// second copy could not drift from it because there is no second copy.
//
// It is deliberately ignorant of everything security-shaped. It knows nothing about device classes,
// capabilities or bearers — it is a file server, and every question about who may do what is answered
// by internal/serve on the connection the page then opens. Assets carry no authority, so serving them
// needs none.
//
// The bundle is NOT committed: dist/ holds a single .gitkeep in git and is filled by generate.sh.
// A binary built without it still runs and still serves — it answers with a page that says how to
// build the UI, because a blank screen is a worse way to learn that than a sentence.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:generate ./generate.sh

// dist is the built Angular bundle.
//
// The all: prefix is load-bearing twice over. It is what lets a bare clone compile at all: a plain
// //go:embed dist would skip names beginning with "." or "_", match the lone .gitkeep and nothing
// else, and fail the build for matching no files. It also carries the bundle's own dotfiles, which a
// static site is entitled to have.
//
//go:embed all:dist
var dist embed.FS

// indexPath is the SPA entry point, and the marker for whether a bundle is present at all.
const indexPath = "dist/index.html"

// Available reports whether a real bundle was built into this binary.
func Available() bool {
	_, err := fs.Stat(dist, indexPath)
	return err == nil
}

// Handler serves the bundle as a single-page app: a request that names a real file gets that file,
// and anything else gets index.html so the Angular router can resolve it client-side.
//
// The fallback is not a convenience. The app's routes are history-API paths (/app/chat/<id>), so a
// reload or a shared link arrives here as a path with no file behind it; answering 404 would break
// every deep link the UI hands out.
//
// Nothing is cached — see handlerFor for why that is the whole policy rather than a simplification.
func Handler() http.Handler {
	if !Available() {
		return http.HandlerFunc(unavailable)
	}
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// dist is embedded above, so this cannot fail at run time; a panic here means the package was
		// edited into an inconsistent state, which is a build-time mistake, not a request-time one.
		panic("webui: embedded dist is not a directory: " + err.Error())
	}
	return handlerFor(sub)
}

// handlerFor is Handler over any filesystem. It exists so the routing rules above can be tested
// against a bundle a test controls: what is embedded here depends on whether anyone ran generate.sh,
// and a test whose assertions change with that is a test that proves nothing on CI.
func handlerFor(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only reads. This handler is the catch-all, so without it a POST to a MISTYPED protocol
		// endpoint — /regsiter — would fall through to the SPA fallback and come back 200 with a page
		// of HTML, which is the least useful way to learn you typed the path wrong.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Nothing here is cached, and that is the whole policy.
		//
		// This is a LAN server: the bundle is a megabyte over a link that carries it in a moment, and
		// the app is a single page, so the cost lands on a hard reload rather than on anything the user
		// does inside it. Weighed against that, caching buys little and costs a whole class of bug —
		// "I changed it and nothing happened", which is the worst kind because the code is right and
		// the browser is lying.
		//
		// It also cannot be done well here. embed.FS reports a zero ModTime, so ServeContent emits no
		// Last-Modified, and nothing sets an ETag — there is no validator, so even a revalidating
		// header degrades to a full refetch. The alternative was to cache by filename, trusting that
		// Angular hashes what it compiles; that held for the compiled chunks and silently did not for
		// anything copied out of public/, where favicon.ico and mascot.png keep their names across
		// builds and would have been pinned for a year.
		//
		// If this ever needs to be fast offline, the answer is Angular's own service worker, which does
		// versioned app-shell caching and update detection properly. Note it needs a secure context:
		// available on localhost, and on the LAN only once there is TLS.
		w.Header().Set("Cache-Control", "no-cache")

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		// index.html goes through serveIndex even though it IS a real file: http.FileServerFS answers
		// /index.html with a 301 to /, which is harmless but a surprise on the app's own entry point.
		if name == "" || name == "index.html" || !exists(fsys, name) {
			serveIndex(w, fsys)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// exists reports whether name is a regular file in the bundle. A directory counts as absent: the SPA
// has no directory listings, and serving one would leak the bundle's shape for nothing.
func exists(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}

// serveIndex writes index.html as the SPA entry point, uncached.
func serveIndex(w http.ResponseWriter, fsys fs.FS) {
	body, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, "web UI unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// No ServeContent modtime: everything embedded reports the zero time, so a conditional request
	// would be answered from a timestamp that says nothing about the build.
	_, _ = w.Write(body)
}

// unavailable answers every request when no bundle was built in.
//
// 503 rather than 404: the route exists and the daemon is fine, the assets are simply not here yet.
// The page says which command fixes it, because the alternative — a blank tab — is how "I built it
// and nothing happened" turns into an afternoon.
func unavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(stubPage))
}

const stubPage = `<!doctype html>
<meta charset="utf-8">
<title>nocturn — web UI not built</title>
<style>
  body { font: 16px/1.6 system-ui, sans-serif; max-width: 42rem; margin: 4rem auto; padding: 0 1.5rem }
  code { background: #eee; padding: .1rem .35rem; border-radius: .2rem }
  pre { background: #eee; padding: 1rem; border-radius: .4rem; overflow-x: auto }
</style>
<h1>The web UI is not in this binary</h1>
<p>The daemon is running — this is only the browser front-end, which is built from
<code>mobile/</code> and embedded at compile time. Build it and rebuild:</p>
<pre>cd mobile &amp;&amp; npm ci &amp;&amp; npm run build
cd .. &amp;&amp; go generate ./internal/webui/ &amp;&amp; go build ./cmd/nocturn</pre>
<p>The companion app and the terminal UI are unaffected.</p>
`
