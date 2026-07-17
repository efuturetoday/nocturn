package script_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// End to end through the REAL gateway: a JS script calls nocturn.call("http.read",
// …), the one gate dispatches to netcap.Net's http.read tool, which runs the
// full guarded path (Guard.Authorize → Fetch), and the HTTP body flows back into
// the script. This is the whole "gateway-backed effect gate, wired" claim proven
// with the actual interpreter, not a fake tool.
func TestE2E_ScriptFetchesThroughGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload-from-server"))
	}))
	defer srv.Close()

	netCap := autoAllowNet(srv.Client())
	r := script.New(tool.NewRegistry().AddMany(netCap.Tools()...))

	src := `
		const resp = JSON.parse(nocturn.call("http.read", {url: ` + jsString(srv.URL) + `}));
		console.log("got: " + resp.body + " (" + resp.status + ")");
	`
	out, err := r.Run(context.Background(), src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "got: payload-from-server (200)") {
		t.Fatalf("stdout = %q, want the server payload + status", out)
	}
}

// The same wiring, deny-by-default: no policy rule matches, so Guard.Authorize
// returns ErrDenied, the tool's Invoke errors, and the script sees a catchable
// exception — the request never leaves the process.
func TestE2E_DeniedRequestNeverLeaves(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()

	// A Net with an empty policy → deny-by-default.
	netCap := netcap.New(
		&gateway.Guard{Policy: capability.Policy{}}, netcap.WithScanner(

			secret.NewScanner(secret.NewStore())), netcap.WithHTTPClient(

			srv.Client()))

	r := script.New(tool.NewRegistry().AddMany(netCap.Tools()...))

	src := `
		try {
			nocturn.call("http.read", {url: ` + jsString(srv.URL) + `});
			console.log("REACHED");
		} catch (e) {
			console.log("blocked");
		}
	`
	out, err := r.Run(context.Background(), src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "REACHED") || !strings.Contains(out, "blocked") {
		t.Fatalf("stdout = %q, want the request blocked", out)
	}
	if hits != 0 {
		t.Fatalf("server was hit %d times, want 0 (denied before egress)", hits)
	}
}

// autoAllowNet builds a Net that auto-allows http.read for any host — enough to
// prove the wiring without an approval loop.
func autoAllowNet(client *http.Client) *netcap.Net {
	return netcap.New(
		&gateway.Guard{
			Policy: capability.Policy{Rules: []capability.Rule{
				{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
			}},
		}, netcap.WithScanner(

			secret.NewScanner(secret.NewStore())), netcap.WithHTTPClient(

			client))

}

// jsString renders s as a JS string literal (quotes + escapes) for embedding in
// script source.
func jsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
