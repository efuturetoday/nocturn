package tools_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
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
