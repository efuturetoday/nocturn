package gateway_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/secret"
)

func allowFetch(hostGlob string) capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Capability: "net.fetch", HostGlob: hostGlob, Effect: capability.Allow, Epoch: capability.Permanent},
	}}
}

func askFetch() capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Capability: "net.fetch", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
}

// autoNotifier resolves the pending request immediately (no phone, no goroutine)
// — Request calls Notify synchronously and reads the buffered outcome. It picks
// the option matching its desired outcome.
type autoNotifier struct {
	want    hitl.Outcome
	resolve func(token string) error
	calls   *int // optional: counts how often the human was asked
}

func (n *autoNotifier) Notify(_ string, options []hitl.Option) error {
	if n.calls != nil {
		*n.calls++
	}
	for _, o := range options {
		if o.Outcome == n.want {
			return n.resolve(o.Token)
		}
	}
	return errors.New("autoNotifier: no matching option")
}

func askEngine(approve bool) *hitl.Engine {
	want := hitl.Denied
	if approve {
		want = hitl.Approved
	}
	return askEngineWant(want)
}

func askEngineWant(want hitl.Outcome) *hitl.Engine {
	n := &autoNotifier{want: want}
	e := hitl.NewEngine([]byte("test-key"), n)
	n.resolve = e.Resolve
	return e
}

func TestFetch_Allow_ReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	n := &gateway.Net{Guard: &gateway.Guard{Policy: allowFetch(capability.Wildcard)}}
	body, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(body) != "pong" {
		t.Fatalf("body = %q, want pong", body)
	}
}

func TestFetch_Deny_DoesNotHitNetwork(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit = true
	}))
	defer srv.Close()

	n := &gateway.Net{Guard: &gateway.Guard{Policy: capability.Policy{}}} // empty = deny-by-default
	_, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL})
	if !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if hit {
		t.Fatal("a denied fetch must not reach the network")
	}
}

func TestFetch_HostAllowlist_DeniesOtherHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	// Only example.com is allowed; the test server's host is 127.0.0.1.
	n := &gateway.Net{Guard: &gateway.Guard{Policy: allowFetch("example.com")}}
	_, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL})
	if !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("fetch to non-allowlisted host: err = %v, want ErrDenied", err)
	}
}

func TestFetch_Ask_ApprovePerformsRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	n := &gateway.Net{Guard: &gateway.Guard{Policy: askFetch(), Approvals: askEngine(true), TTL: time.Second}}
	body, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("approved fetch: %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

func TestFetch_Ask_DenyBlocksRequest(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit = true
	}))
	defer srv.Close()

	n := &gateway.Net{Guard: &gateway.Guard{Policy: askFetch(), Approvals: askEngine(false), TTL: time.Second}}
	_, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL})
	if !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if hit {
		t.Fatal("a denied approval must not reach the network")
	}
}

// "Allow this session" remembers the grant, bound to the session epoch: the
// same host is not asked again — until the epoch is closed, which revokes it.
func TestFetch_AllowThisSession_EpochBoundGrantAndRevocation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	asked := 0
	n := &autoNotifier{want: hitl.ApprovedSession, calls: &asked}
	engine := hitl.NewEngine([]byte("test-key"), n)
	n.resolve = engine.Resolve

	epochs := capability.NewEpochRegistry()
	epoch := epochs.Open()
	g := &gateway.Net{Guard: &gateway.Guard{Policy: askFetch(), Approvals: engine, Epochs: epochs, TTL: time.Second}}
	ctx := capability.WithEpoch(context.Background(), epoch)

	// first call: asked, ApprovedSession granted for this host, bound to epoch
	if _, err := g.Fetch(ctx, secret.Request{URL: srv.URL}); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	// second call to the same host: covered by the live session grant, no ask
	if _, err := g.Fetch(ctx, secret.Request{URL: srv.URL}); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if asked != 1 {
		t.Fatalf("human asked %d times, want 1 (the session grant should skip the 2nd ask)", asked)
	}

	// close the epoch: the grant is revoked, so the same host is asked again.
	epochs.Close(epoch)
	if _, err := g.Fetch(ctx, secret.Request{URL: srv.URL}); err != nil {
		t.Fatalf("third fetch: %v", err)
	}
	if asked != 2 {
		t.Fatalf("human asked %d times, want 2 (closing the epoch must revoke the session grant)", asked)
	}
}

func hostFromURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Hostname()
}

// The gateway injects the credential host-side for the bound destination; the
// caller's request carried none, yet the server sees the bearer.
func TestFetch_InjectsCredentialForBoundHost(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	v := secret.NewStore()
	v.Set("ms_graph", []byte("abc123"))
	in := secret.NewInjector(v, secret.Binding{
		Secret: "ms_graph", Host: hostFromURL(t, srv.URL), Header: "Authorization", Prefix: "Bearer ",
	})

	n := &gateway.Net{Guard: &gateway.Guard{Policy: allowFetch(capability.Wildcard)}, Credentials: in}
	if _, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotAuth != "Bearer abc123" {
		t.Fatalf("server saw Authorization %q, want Bearer abc123", gotAuth)
	}
}

// A credential bound to a different host does not ride along to this one.
func TestFetch_NoInjectForOtherHost(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	v := secret.NewStore()
	v.Set("ms_graph", []byte("abc123"))
	in := secret.NewInjector(v, secret.Binding{
		Secret: "ms_graph", Host: "graph.microsoft.com", Header: "Authorization", Prefix: "Bearer ",
	})

	n := &gateway.Net{Guard: &gateway.Guard{Policy: allowFetch(capability.Wildcard)}, Credentials: in}
	if _, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("server saw Authorization %q, want none (credential bound to another host)", gotAuth)
	}
}

// A guest-built request that carries its own credential — userinfo in the URL or
// a sensitive header — is rejected before any network call.
func TestFetch_ManualCredential_Rejected(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()

	n := &gateway.Net{Guard: &gateway.Guard{Policy: allowFetch(capability.Wildcard)}}

	u, _ := url.Parse(srv.URL)
	if _, err := n.Fetch(context.Background(), secret.Request{URL: "http://user:pass@" + u.Host + "/"}); !errors.Is(err, gateway.ErrManualCredential) {
		t.Fatalf("userinfo: err = %v, want ErrManualCredential", err)
	}
	if _, err := n.Fetch(context.Background(),
		secret.Request{URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer x"}}); !errors.Is(err, gateway.ErrManualCredential) {
		t.Fatalf("header: err = %v, want ErrManualCredential", err)
	}
	if hit {
		t.Fatal("a request with a manual credential must not reach the network")
	}
}

// A POST carries method, body, and Content-Type through to the server.
func TestFetch_PostSendsBody(t *testing.T) {
	var gotMethod, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	n := &gateway.Net{Guard: &gateway.Guard{Policy: allowFetch(capability.Wildcard)}}
	_, err := n.Fetch(context.Background(), secret.Request{
		Method: "POST", URL: srv.URL, Body: []byte(`{"x":1}`),
		Headers: map[string]string{"Content-Type": "application/json"},
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if gotMethod != "POST" || gotCT != "application/json" || gotBody != `{"x":1}` {
		t.Fatalf("server saw method=%q ct=%q body=%q", gotMethod, gotCT, gotBody)
	}
}

// The net.fetch tool sends a POST (method lower-cased by the model is accepted)
// with body + content type, and rejects an unsupported method before any request.
func TestFetchTool_Post(t *testing.T) {
	var gotMethod, gotCT, gotBody string
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	n := &gateway.Net{Guard: &gateway.Guard{Policy: allowFetch(capability.Wildcard)}}
	var fetch brain.Tool
	for _, tl := range n.Tools() {
		if tl.Name == "net.fetch" {
			fetch = tl
		}
	}

	args := fmt.Sprintf(`{"url":%q,"method":"post","body":"hi","content_type":"text/plain"}`, srv.URL)
	if _, err := fetch.Invoke(context.Background(), args); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if gotMethod != "POST" || gotCT != "text/plain" || gotBody != "hi" {
		t.Fatalf("server saw method=%q ct=%q body=%q", gotMethod, gotCT, gotBody)
	}

	hit = false
	if _, err := fetch.Invoke(context.Background(), fmt.Sprintf(`{"url":%q,"method":"TRACE"}`, srv.URL)); err == nil {
		t.Fatal("expected error for unsupported method")
	}
	if hit {
		t.Fatal("an unsupported method must be rejected before any request")
	}
}
