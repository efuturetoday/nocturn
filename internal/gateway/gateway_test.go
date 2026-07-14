package gateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	body, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL}, nil)
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
	_, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL}, nil)
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
	_, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL}, nil)
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
	body, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL}, nil)
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
	_, err := n.Fetch(context.Background(), secret.Request{URL: srv.URL}, nil)
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
	if _, err := g.Fetch(ctx, secret.Request{URL: srv.URL}, nil); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	// second call to the same host: covered by the live session grant, no ask
	if _, err := g.Fetch(ctx, secret.Request{URL: srv.URL}, nil); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if asked != 1 {
		t.Fatalf("human asked %d times, want 1 (the session grant should skip the 2nd ask)", asked)
	}

	// close the epoch: the grant is revoked, so the same host is asked again.
	epochs.Close(epoch)
	if _, err := g.Fetch(ctx, secret.Request{URL: srv.URL}, nil); err != nil {
		t.Fatalf("third fetch: %v", err)
	}
	if asked != 2 {
		t.Fatalf("human asked %d times, want 2 (closing the epoch must revoke the session grant)", asked)
	}
}

// The gateway injects the secret host-side; the caller's request carried no
// credential, yet the server sees the bearer.
func TestFetch_InjectsCredentialAtBoundary(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	v := secret.NewStore()
	v.Set("ms_graph", []byte("abc123"))

	n := &gateway.Net{Guard: &gateway.Guard{Policy: allowFetch(capability.Wildcard)}, Secrets: v}
	_, err := n.Fetch(context.Background(),
		secret.Request{URL: srv.URL}, // no credential in the caller's request
		&secret.Binding{Secret: "ms_graph", Header: "Authorization", Prefix: "Bearer "})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotAuth != "Bearer abc123" {
		t.Fatalf("server saw Authorization %q, want Bearer abc123", gotAuth)
	}
}
