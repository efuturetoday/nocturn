package netcap_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

func allowRead(hostGlob string) capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Family: "http", TargetGlob: hostGlob, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
	}}
}

func askRead() capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
}

// allowReadWrite permits both reads and writes over http (for write/POST tests).
func allowReadWrite() capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent},
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

	n := netcap.New(&gateway.Guard{Policy: allowRead(capability.Wildcard)})
	resp, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(resp.Body) != "pong" {
		t.Fatalf("body = %q, want pong", resp.Body)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
}

func TestFetch_Deny_DoesNotHitNetwork(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit = true
	}))
	defer srv.Close()

	n := netcap.New(&gateway.Guard{Policy: capability.Policy{}}) // empty = deny-by-default
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
	n := netcap.New(&gateway.Guard{Policy: allowRead("example.com")})
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

	n := netcap.New(&gateway.Guard{Policy: askRead(), Approvals: askEngine(true), TTL: time.Second})
	resp, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("approved fetch: %v", err)
	}
	if string(resp.Body) != "ok" {
		t.Fatalf("body = %q, want ok", resp.Body)
	}
}

func TestFetch_Ask_DenyBlocksRequest(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit = true
	}))
	defer srv.Close()

	n := netcap.New(&gateway.Guard{Policy: askRead(), Approvals: askEngine(false), TTL: time.Second})
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

	guard := &gateway.Guard{Policy: askRead(), Approvals: engine, TTL: time.Second}
	g := netcap.New(guard)
	// A revocable scope owns the session grant; the Guard owns its epoch, so the test
	// never touches the registry — it binds the scope and later revokes it.
	scope := guard.NewScope(gateway.Authority{})
	ctx := scope.Bind(context.Background())

	// first call: asked, ApprovedSession recorded on the scope's grants for this host
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

	// revoke the scope: the grant is revoked, so the same host is asked again.
	scope.Revoke()
	if _, err := g.Fetch(ctx, secret.Request{URL: srv.URL}); err != nil {
		t.Fatalf("third fetch: %v", err)
	}
	if asked != 2 {
		t.Fatalf("human asked %d times, want 2 (revoking the scope must revoke the session grant)", asked)
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

	n := netcap.New(&gateway.Guard{Policy: allowRead(capability.Wildcard)}, netcap.WithCredentials(in))
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

	n := netcap.New(&gateway.Guard{Policy: allowRead(capability.Wildcard)}, netcap.WithCredentials(in))
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

	n := netcap.New(&gateway.Guard{Policy: allowRead(capability.Wildcard)})

	u, _ := url.Parse(srv.URL)
	if _, err := n.Fetch(context.Background(), secret.Request{URL: "http://user:pass@" + u.Host + "/"}); !errors.Is(err, netcap.ErrManualCredential) {
		t.Fatalf("userinfo: err = %v, want ErrManualCredential", err)
	}
	if _, err := n.Fetch(context.Background(),
		secret.Request{URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer x"}}); !errors.Is(err, netcap.ErrManualCredential) {
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

	n := netcap.New(&gateway.Guard{Policy: allowReadWrite()})
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

// A write (POST → http.write) is gated separately: with only http.read allowed,
// it is denied by default and never reaches the network.
func TestFetch_Write_DeniedWithoutWriteRule(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()

	n := netcap.New(&gateway.Guard{Policy: allowRead(capability.Wildcard)})
	_, err := n.Fetch(context.Background(), secret.Request{Method: "POST", URL: srv.URL, Body: []byte("x")})
	if !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if hit {
		t.Fatal("a write with no http.write rule must not reach the network")
	}
}

// A stored vault value in the outbound request is blocked by the leak scanner
// before it reaches the network.
func TestFetch_EgressLeak_Blocked(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()

	store := secret.NewStore()
	store.Set("tok", []byte("supersecretvalue123"))
	n := netcap.New(&gateway.Guard{Policy: allowRead(capability.Wildcard)}, netcap.WithScanner(secret.NewScanner(store)))

	_, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL + "/?x=supersecretvalue123"})
	if !errors.Is(err, secret.ErrLeaked) {
		t.Fatalf("err = %v, want ErrLeaked", err)
	}
	if hit {
		t.Fatal("a leaking request must not reach the network")
	}
}

// A vault value echoed back in a response is redacted before it is returned.
func TestFetch_IngressLeak_Redacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("echo supersecretvalue123 back"))
	}))
	defer srv.Close()

	store := secret.NewStore()
	store.Set("tok", []byte("supersecretvalue123"))
	n := netcap.New(&gateway.Guard{Policy: allowRead(capability.Wildcard)}, netcap.WithScanner(secret.NewScanner(store)))

	resp, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if strings.Contains(string(resp.Body), "supersecretvalue123") {
		t.Fatal("ingress secret was not redacted")
	}
	if !strings.Contains(string(resp.Body), "[REDACTED]") {
		t.Fatal("no redaction marker in response body")
	}
}

// A vault value echoed back in a RESPONSE HEADER is redacted too (the envelope now
// exposes headers to the model, so they must be scanned like the body).
func TestFetch_IngressLeak_RedactedInHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", "supersecretvalue123")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	store := secret.NewStore()
	store.Set("tok", []byte("supersecretvalue123"))
	n := netcap.New(&gateway.Guard{Policy: allowRead(capability.Wildcard)}, netcap.WithScanner(secret.NewScanner(store)))

	resp, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if strings.Contains(resp.Headers["X-Echo"], "supersecretvalue123") {
		t.Fatalf("header secret not redacted: %q", resp.Headers["X-Echo"])
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

	n := netcap.New(&gateway.Guard{Policy: allowReadWrite()})
	var write tool.Tool
	for _, tl := range n.Tools() {
		if tl.Name == "http.write" {
			write = tl
		}
	}

	args := fmt.Sprintf(`{"url":%q,"method":"post","body":"hi","content_type":"text/plain"}`, srv.URL)
	if _, err := write.Invoke(context.Background(), args); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if gotMethod != "POST" || gotCT != "text/plain" || gotBody != "hi" {
		t.Fatalf("server saw method=%q ct=%q body=%q", gotMethod, gotCT, gotBody)
	}

	hit = false
	if _, err := write.Invoke(context.Background(), fmt.Sprintf(`{"url":%q,"method":"TRACE"}`, srv.URL)); err == nil {
		t.Fatal("expected error for unsupported method")
	}
	if hit {
		t.Fatal("an unsupported method must be rejected before any request")
	}
}
