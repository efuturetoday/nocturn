package tools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/tools"
)

// httpRead returns the http_read base tool.
func httpRead(t *testing.T) agentkit.Tool {
	t.Helper()
	ts, err := tools.Base(tools.Config{})
	if err != nil {
		t.Fatalf("Base: %v", err)
	}
	for _, tl := range ts {
		if tl.Spec().Name == "http_read" {
			return tl
		}
	}
	t.Fatal("http_read not found in Base")
	return nil
}

// hostOf parses a test-server URL and returns its host:port.
func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Host
}

// TestNet_RedirectReGated is the HIGH-2 guarantee: a redirect to a host the policy does NOT allow is
// blocked at the hop — the redirect is a fresh gated request, not a free pass. Without re-gating, a
// 302 from an allowed host to an internal/attacker host would bypass the allowlist entirely.
func TestNet_RedirectReGated(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("SECRET-BODY"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	deniedHost := hostOf(t, target.URL)
	// Policy: allow everything except the redirect target's host.
	pol := gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		if a.Kind == tools.NetKind && a.Target == deniedHost {
			return gate.Denied()
		}
		return gate.Allowed()
	})
	ctx := gate.With(context.Background(), pol, nil, nil)

	read := httpRead(t)
	out, err := read.Call(ctx, `{"url":`+jsonQuote(source.URL)+`}`)
	if err == nil {
		t.Fatalf("redirect to denied host was followed, out=%q", out)
	}
	if strings.Contains(out, "SECRET-BODY") {
		t.Fatalf("redirect target body leaked despite the block: %q", out)
	}
}

// TestNet_RedirectAllowedFollowed is the companion: when the policy allows the redirect target, the
// follow proceeds and the final body comes back — re-gating must not break legitimate redirects.
func TestNet_RedirectAllowedFollowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("FINAL-BODY"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	// Allow-all policy.
	ctx := gate.With(context.Background(), gate.PolicyFunc(func(gate.Action) gate.Ruling {
		return gate.Allowed()
	}), nil, nil)

	read := httpRead(t)
	out, err := read.Call(ctx, `{"url":`+jsonQuote(source.URL)+`}`)
	if err != nil {
		t.Fatalf("allowed redirect was blocked: %v", err)
	}
	if !strings.Contains(out, "FINAL-BODY") {
		t.Fatalf("expected the redirected body, got %q", out)
	}
}

// TestNet_StripsCredentialHeaders is the header-leak fix: credential-bearing response headers
// (Set-Cookie, WWW-Authenticate, …) are dropped from the JSON envelope before it reaches the model,
// so an authenticating/reflecting endpoint cannot launder a credential into the transcript.
func TestNet_StripsCredentialHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "session=DEADBEEF; HttpOnly")
		w.Header().Set("WWW-Authenticate", "Bearer realm=secret")
		w.Header().Set("X-Safe", "ok")
		w.Write([]byte("body"))
	}))
	defer srv.Close()

	read := httpRead(t)
	out, err := read.Call(context.Background(), `{"url":`+jsonQuote(srv.URL)+`}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, leaked := range []string{"DEADBEEF", "Set-Cookie", "WWW-Authenticate", "Bearer realm"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("credential header leaked to the model: %q found in %q", leaked, out)
		}
	}
	if !strings.Contains(out, "X-Safe") {
		t.Fatalf("a safe header was dropped: %q", out)
	}
}

// TestNet_Read_RejectsNonSafeMethod is the read-vs-write split at the http_read boundary: only safe
// methods (GET/HEAD) pass; a mutating method is refused BEFORE any host is contacted, so the model
// cannot smuggle a write through the read tool.
func TestNet_Read_RejectsNonSafeMethod(t *testing.T) {
	read := httpRead(t)
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		t.Run(m, func(t *testing.T) {
			out, err := read.Call(context.Background(), `{"url":"https://example.com","method":`+jsonQuote(m)+`}`)
			if err == nil {
				t.Fatalf("http_read accepted mutating method %q, out=%q", m, out)
			}
			if !strings.Contains(err.Error(), "only GET/HEAD") {
				t.Fatalf("unclear method error: %v", err)
			}
		})
	}
}

// TestNet_Write_RejectsNonMutatingMethod is the mirror: http_write refuses a safe method, so the two
// tools stay a clean intent split (a GET never rides the write path that may ask for approval).
func TestNet_Write_RejectsNonMutatingMethod(t *testing.T) {
	write := toolFrom(t, tools.Config{}, "http_write")
	for _, m := range []string{"GET", "HEAD"} {
		t.Run(m, func(t *testing.T) {
			out, err := write.Call(context.Background(), `{"url":"https://example.com","method":`+jsonQuote(m)+`}`)
			if err == nil {
				t.Fatalf("http_write accepted safe method %q, out=%q", m, out)
			}
			if !strings.Contains(err.Error(), "only POST/PUT/PATCH/DELETE") {
				t.Fatalf("unclear method error: %v", err)
			}
		})
	}
}

// TestNet_Do_GateDeniesUnknownHost is the gate-before-effect guarantee: a denied host never reaches
// the network — the HTTP handler is never invoked.
func TestNet_Do_GateDeniesUnknownHost(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
		w.Write([]byte("REACHED"))
	}))
	defer srv.Close()

	read := httpRead(t)
	out, err := read.Call(denyAll(context.Background()), `{"url":`+jsonQuote(srv.URL)+`}`)
	if err == nil {
		t.Fatalf("denied host was fetched, out=%q", out)
	}
	if hit.Load() {
		t.Fatal("handler was invoked despite the gate denial — effect ran before the gate")
	}
}

// TestNet_Do_GateAllowsGrantedHost is the companion: an allowed host is fetched and its body returns.
func TestNet_Do_GateAllowsGrantedHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("OK-BODY"))
	}))
	defer srv.Close()

	read := httpRead(t)
	out, err := read.Call(allowAll(context.Background()), `{"url":`+jsonQuote(srv.URL)+`}`)
	if err != nil {
		t.Fatalf("allowed host was blocked: %v", err)
	}
	if !strings.Contains(out, "OK-BODY") {
		t.Fatalf("expected the body, got %q", out)
	}
}

// TestNet_Egress_BlocksSmuggledSecret is the exfil guard: a stored vault value smuggled into the URL
// is blocked by the egress scan BEFORE the request goes out, so the handler is never reached — and
// the block happens before any host-owned credential is injected, so the app's own bearer can never
// be mistaken for the leak.
func TestNet_Egress_BlocksSmuggledSecret(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
	}))
	defer srv.Close()

	store := secret.NewStore()
	store.Set("api", []byte("SUPERSECRETVALUE123"))
	sc := secret.NewScanner(store)

	read := toolFrom(t, tools.Config{Scanner: sc}, "http_read")
	out, err := read.Call(allowAll(context.Background()), `{"url":`+jsonQuote(srv.URL+"/?token=SUPERSECRETVALUE123")+`}`)
	if err == nil {
		t.Fatalf("smuggled secret in the URL was not blocked, out=%q", out)
	}
	if strings.Contains(err.Error(), "SUPERSECRETVALUE123") {
		t.Fatalf("egress error leaked the secret value: %v", err)
	}
	if hit.Load() {
		t.Fatal("handler was reached despite the egress block")
	}
}

// TestNet_InjectsBearerAtBorder_NotVisibleToCaller is the credential-injection guarantee: a host-owned
// bearer bound to the destination host is stamped in host-side (the caller never named it), yet it
// never appears in the JSON envelope handed back.
func TestNet_InjectsBearerAtBorder_NotVisibleToCaller(t *testing.T) {
	gotAuth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	store := secret.NewStore()
	store.Set("tok", []byte("SECRET-BEARER-XYZ"))
	inj := secret.NewInjector(store, secret.Binding{
		Secret: "tok", Host: hostOf(t, srv.URL), Header: "Authorization", Prefix: "Bearer ",
	})

	read := toolFrom(t, tools.Config{Secrets: inj}, "http_read")
	out, err := read.Call(allowAll(context.Background()), `{"url":`+jsonQuote(srv.URL)+`}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := <-gotAuth; got != "Bearer SECRET-BEARER-XYZ" {
		t.Fatalf("bearer not injected at the border: %q", got)
	}
	if strings.Contains(out, "SECRET-BEARER-XYZ") {
		t.Fatalf("injected bearer leaked back to the caller: %q", out)
	}
}

// TestNet_Ingress_RedactsEchoedSecret is the reflection guard: a stored value echoed back in the
// response body is redacted before the envelope reaches the model.
func TestNet_Ingress_RedactsEchoedSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("here is your token=SUPERSECRETVALUE123 ok"))
	}))
	defer srv.Close()

	store := secret.NewStore()
	store.Set("api", []byte("SUPERSECRETVALUE123"))
	sc := secret.NewScanner(store)

	read := toolFrom(t, tools.Config{Scanner: sc}, "http_read")
	out, err := read.Call(allowAll(context.Background()), `{"url":`+jsonQuote(srv.URL)+`}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(out, "SUPERSECRETVALUE123") {
		t.Fatalf("echoed secret was not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %q", out)
	}
}

// TestNet_InvalidURL_Rejected covers the URL guard: empty, a non-http scheme, and a host-less URL are
// refused before any gate or fetch.
func TestNet_InvalidURL_Rejected(t *testing.T) {
	read := httpRead(t)
	cases := []struct{ name, url string }{
		{"empty", ""},
		{"file-scheme", "file:///etc/passwd"},
		{"ftp-scheme", "ftp://ftp.example.com/x"},
		{"no-host", "http://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A deny-all gate would also error; use allow-all so ONLY the URL guard can fail it, proving
			// the rejection is the URL check, not the gate.
			out, err := read.Call(allowAll(context.Background()), `{"url":`+jsonQuote(tc.url)+`}`)
			if err == nil {
				t.Fatalf("invalid url %q accepted, out=%q", tc.url, out)
			}
		})
	}
}

// TestNet_HostMatch_Semantics pins the exported host matcher shared across the net-axis tools: "*"
// covers anything, an exact host matches itself, and "*.domain" covers the domain and its subdomains
// but not a different domain.
func TestNet_HostMatch_Semantics(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"*", "anything.com", true},
		{"example.com", "example.com", true},
		{"example.com", "api.example.com", false},
		{"*.example.com", "example.com", true},
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.org", false},
		{"*.example.com", "notexample.com", false},
		{"", "example.com", false},
	}
	for _, tc := range cases {
		if got := tools.HostMatch(tc.pattern, tc.host); got != tc.want {
			t.Errorf("HostMatch(%q,%q)=%v want %v", tc.pattern, tc.host, got, tc.want)
		}
	}
}

// TestNet_Suggestions_ParentDomainWidening pins the one widening the human is offered: the parent
// domain wildcard for a subdomain, and none for a bare two-label domain.
func TestNet_Suggestions_ParentDomainWidening(t *testing.T) {
	got := tools.NetSuggestions("api.example.com")
	if len(got) != 1 || got[0].Kind != tools.NetKind || got[0].Target != "*.example.com" {
		t.Fatalf("subdomain widening = %+v, want [{net *.example.com}]", got)
	}
	if s := tools.NetSuggestions("example.com"); s != nil {
		t.Fatalf("a bare domain should offer no widening, got %+v", s)
	}
}

// TestNet_Do_JSONEnvelopeShape pins the {status, statusText, headers, body} envelope both net tools
// return, so a caller sees the real outcome (a 404 is not mistaken for success).
func TestNet_Do_JSONEnvelopeShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Safe", "yes")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("nope"))
	}))
	defer srv.Close()

	read := httpRead(t)
	out, err := read.Call(context.Background(), `{"url":`+jsonQuote(srv.URL)+`}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env struct {
		Status     int               `json:"status"`
		StatusText string            `json:"statusText"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope not JSON: %v (%q)", err, out)
	}
	if env.Status != http.StatusNotFound || env.Body != "nope" || env.Headers["X-Safe"] != "yes" {
		t.Fatalf("envelope mismatch: %+v", env)
	}
}

// TestNet_Do_BodyCappedAt64KiB proves a huge response can't blow up context: the body handed back is
// capped at 64 KiB.
func TestNet_Do_BodyCappedAt64KiB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, 256*1024)) // 256 KiB of zeros
	}))
	defer srv.Close()

	read := httpRead(t)
	out, err := read.Call(context.Background(), `{"url":`+jsonQuote(srv.URL)+`}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	if len(env.Body) != 1<<16 {
		t.Fatalf("body cap not enforced: got %d bytes, want %d", len(env.Body), 1<<16)
	}
}

// TestNet_FirstHeaders_KeepsFirstValueOnly proves a multi-valued response header collapses to its
// first value in the envelope (the header map is one-value-per-name).
func TestNet_FirstHeaders_KeepsFirstValueOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("X-Multi", "first")
		w.Header().Add("X-Multi", "second")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	read := httpRead(t)
	out, err := read.Call(context.Background(), `{"url":`+jsonQuote(srv.URL)+`}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	if env.Headers["X-Multi"] != "first" {
		t.Fatalf("multi-valued header kept %q, want first only", env.Headers["X-Multi"])
	}
}

// TestNet_Do_DefaultContentType proves a write with a body but no content_type defaults to
// application/json.
func TestNet_Do_DefaultContentType(t *testing.T) {
	gotCT := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT <- r.Header.Get("Content-Type")
	}))
	defer srv.Close()

	write := toolFrom(t, tools.Config{}, "http_write")
	if _, err := write.Call(context.Background(), `{"url":`+jsonQuote(srv.URL)+`,"body":"{}"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if ct := <-gotCT; ct != "application/json" {
		t.Fatalf("default content type = %q, want application/json", ct)
	}
}

// TestNet_NilInjectorNilScanner_StillWorks proves the network tool degrades gracefully: with neither
// injector nor scanner a plain fetch still succeeds.
func TestNet_NilInjectorNilScanner_StillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("plain"))
	}))
	defer srv.Close()

	read := toolFrom(t, tools.Config{}, "http_read") // no Secrets, no Scanner
	out, err := read.Call(context.Background(), `{"url":`+jsonQuote(srv.URL)+`}`)
	if err != nil || !strings.Contains(out, "plain") {
		t.Fatalf("plain fetch failed: out=%q err=%v", out, err)
	}
}

// TestNet_Do_CredentialInjectionError_Fails proves a fail-closed credential path: a binding whose
// secret is absent from the store aborts the request rather than sending it half-authenticated.
func TestNet_Do_CredentialInjectionError_Fails(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
	}))
	defer srv.Close()

	store := secret.NewStore() // "missing" is never Set → resolver returns ErrNotFound
	inj := secret.NewInjector(store, secret.Binding{
		Secret: "missing", Host: hostOf(t, srv.URL), Header: "Authorization", Prefix: "Bearer ",
	})

	read := toolFrom(t, tools.Config{Secrets: inj}, "http_read")
	out, err := read.Call(allowAll(context.Background()), `{"url":`+jsonQuote(srv.URL)+`}`)
	if err == nil {
		t.Fatalf("missing credential did not fail the request, out=%q", out)
	}
	if !strings.Contains(err.Error(), "credential injection") {
		t.Fatalf("unclear injection error: %v", err)
	}
	if hit.Load() {
		t.Fatal("request went out despite the credential failure")
	}
}
