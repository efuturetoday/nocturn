package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/tools"
)

// --- test doubles -----------------------------------------------------------

// recordRT is a RoundTripper that flags whether it was invoked; it should never be reached when a
// request is rejected before the HTTP call (gate deny, invalid args, bad url).
type recordRT struct {
	mu     sync.Mutex
	called bool
}

func (r *recordRT) RoundTrip(*http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.called = true
	r.mu.Unlock()
	return nil, errors.New("recordRT: RoundTrip must not be called")
}

func (r *recordRT) wasCalled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

// errBoom is surfaced by errRT to exercise the transport-error wrap.
var errBoom = errors.New("boom")

type errRT struct{}

func (errRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, errBoom }

// fakeApprover records the action and suggestions it was handed and answers with a fixed verdict.
type fakeApprover struct {
	mu      sync.Mutex
	called  bool
	action  gate.Action
	suggest []gate.Grant
	approve bool
}

func (f *fakeApprover) Ask(_ context.Context, a gate.Action, suggest []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.action = a
	f.suggest = suggest
	return f.approve, gate.Grant{Kind: a.Kind, Target: a.Target}, gate.RecallSession, nil
}

// --- Spec -------------------------------------------------------------------

func TestSpec_Shape(t *testing.T) {
	t.Parallel()
	spec := tools.HTTPGet().Spec()

	if spec.Name != "http_get" {
		t.Errorf("Spec().Name = %q, want %q", spec.Name, "http_get")
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("Spec().Validate() = %v, want nil", err)
	}
	params := spec.Parameters
	if params == nil {
		t.Fatal("Spec().Parameters = nil, want an object schema")
	}
	if params.Type != agentkit.TypeObject {
		t.Errorf("Parameters.Type = %q, want %q", params.Type, agentkit.TypeObject)
	}
	if _, ok := params.Properties["url"]; !ok {
		t.Errorf("Parameters.Properties missing %q; got %v", "url", params.Properties)
	}
	if !reflect.DeepEqual(params.Required, []string{"url"}) {
		t.Errorf("Parameters.Required = %v, want [url]", params.Required)
	}
}

// --- Call: happy paths ------------------------------------------------------

func TestCall_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<body>"))
	}))
	defer srv.Close()

	tool := tools.HTTPGet(tools.WithClient(srv.Client()))
	got, err := tool.Call(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Call() error = %v, want nil", err)
	}
	if want := "200 OK\n<body>"; got != want {
		t.Errorf("Call() = %q, want %q", got, want)
	}
}

// TestCall_UngatedWhenNoGateMachinery: with no gate machinery installed in ctx the request proceeds
// freely — gating is opt-in per install.
func TestCall_UngatedWhenNoGateMachinery(t *testing.T) {
	t.Parallel()
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// context.Background() carries no perms → gate.Check is a no-op.
	_, err := tools.HTTPGet(tools.WithClient(srv.Client())).Call(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Call() error = %v, want nil", err)
	}
	if !hit {
		t.Error("server was not reached; request should proceed when no gate machinery is installed")
	}
}

// TestCall_GateCheckTargetIsHost pins that http_get gates Action{Kind:NetAxis, Target:u.Host} and
// forwards exactly HostSuggestions(host) to the approver.
func TestCall_GateCheckTargetIsHost(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	host := mustHost(t, srv.URL)
	appr := &fakeApprover{approve: true}
	// Ask-policy with an empty grant store forces the approver to be consulted.
	ctx := gate.With(context.Background(),
		gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.AskWith(gate.RecallSession) }),
		gate.NewMemGrants(),
		appr,
	)

	if _, err := tools.HTTPGet(tools.WithClient(srv.Client())).Call(ctx, `{"url":"`+srv.URL+`"}`); err != nil {
		t.Fatalf("Call() error = %v, want nil (approver approves)", err)
	}
	if !appr.called {
		t.Fatal("approver was not asked")
	}
	wantAction := gate.Action{Kind: tools.NetAxis, Target: host}
	if appr.action != wantAction {
		t.Errorf("approver action = %+v, want %+v", appr.action, wantAction)
	}
	if wantSuggest := tools.HostSuggestions(host); !reflect.DeepEqual(appr.suggest, wantSuggest) {
		t.Errorf("approver suggestions = %+v, want %+v", appr.suggest, wantSuggest)
	}
}

// --- Call: gating short-circuits the request --------------------------------

// TestCall_GatesHostBeforeRequest: a policy Deny surfaces ErrDenied and the HTTP client is never
// reached (nor the approver) — the host is gated BEFORE the request.
func TestCall_GatesHostBeforeRequest(t *testing.T) {
	t.Parallel()
	rt := &recordRT{}
	appr := &fakeApprover{approve: true} // would approve, but Deny must short-circuit before it
	ctx := gate.With(context.Background(),
		gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.Denied() }),
		gate.NewMemGrants(),
		appr,
	)

	tool := tools.HTTPGet(tools.WithClient(&http.Client{Transport: rt}))
	_, err := tool.Call(ctx, `{"url":"http://example.com"}`)

	if !errors.Is(err, gate.ErrDenied) {
		t.Errorf("Call() error = %v, want gate.ErrDenied", err)
	}
	if rt.wasCalled() {
		t.Error("HTTP client was called; a denied host must not reach the network")
	}
	if appr.called {
		t.Error("approver was asked; a policy Deny must short-circuit before the approver")
	}
}

// --- Call: body limits ------------------------------------------------------

func TestCall_LimitTruncatesBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 100)))
	}))
	defer srv.Close()

	tool := tools.HTTPGet(tools.WithClient(srv.Client()), tools.WithLimit(10))
	got, err := tool.Call(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Call() error = %v, want nil", err)
	}
	if body := bodyOf(got); len(body) != 10 {
		t.Errorf("body length = %d, want 10 (WithLimit(10)); body=%q", len(body), body)
	}
}

// TestWithLimit_DefaultIs4000 verifies the default read cap through observable behavior: a body larger
// than the default is truncated to 4000 bytes.
func TestWithLimit_DefaultIs4000(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 5000)))
	}))
	defer srv.Close()

	tool := tools.HTTPGet(tools.WithClient(srv.Client())) // no WithLimit → default
	got, err := tool.Call(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Call() error = %v, want nil", err)
	}
	if body := bodyOf(got); len(body) != 4000 {
		t.Errorf("body length = %d, want 4000 (default limit)", len(body))
	}
}

// --- Call: rejection before the request (SSRF guard, bad args) ---------------

func TestCall_InvalidArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    string
		wantSub string
	}{
		{"not json", `this is not json`, "invalid arguments"},
		{"empty object yields empty url", `{}`, "invalid url"},
		{"explicit empty url", `{"url":""}`, "invalid url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rt := &recordRT{}
			tool := tools.HTTPGet(tools.WithClient(&http.Client{Transport: rt}))
			_, err := tool.Call(context.Background(), tt.args)
			if err == nil {
				t.Fatalf("Call(%q) error = nil, want error containing %q", tt.args, tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("Call(%q) error = %q, want substring %q", tt.args, err, tt.wantSub)
			}
			if !strings.HasPrefix(err.Error(), "http_get:") {
				t.Errorf("Call(%q) error = %q, want %q prefix", tt.args, err, "http_get:")
			}
			if rt.wasCalled() {
				t.Errorf("Call(%q) reached the HTTP client; invalid input must be rejected first", tt.args)
			}
		})
	}
}

// TestCall_URLWithoutHostRejected: URLs whose parsed Host is empty (relative paths, file scheme) are
// rejected before any request — the SSRF guard.
func TestCall_URLWithoutHostRejected(t *testing.T) {
	t.Parallel()
	urls := []string{"/relative/path", "file:///etc/passwd", "http://%zz"}
	for _, raw := range urls {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			rt := &recordRT{}
			tool := tools.HTTPGet(tools.WithClient(&http.Client{Transport: rt}))
			args, err := json.Marshal(struct {
				URL string `json:"url"`
			}{raw})
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}
			_, err = tool.Call(context.Background(), string(args))
			if err == nil {
				t.Fatalf("Call(url=%q) error = nil, want invalid url", raw)
			}
			if !strings.Contains(err.Error(), "invalid url") {
				t.Errorf("Call(url=%q) error = %q, want substring %q", raw, err, "invalid url")
			}
			if rt.wasCalled() {
				t.Errorf("Call(url=%q) reached the HTTP client; a host-less URL must not (SSRF guard)", raw)
			}
		})
	}
}

// --- Call: transport error is wrapped ---------------------------------------

// TestCall_TransportErrorSurfaced: a client.Do failure is wrapped with the "http_get:" prefix and the
// underlying error stays inspectable via errors.Is.
func TestCall_TransportErrorSurfaced(t *testing.T) {
	t.Parallel()
	tool := tools.HTTPGet(tools.WithClient(&http.Client{Transport: errRT{}}))
	_, err := tool.Call(context.Background(), `{"url":"http://example.com"}`)
	if err == nil {
		t.Fatal("Call() error = nil, want transport error")
	}
	if !strings.HasPrefix(err.Error(), "http_get:") {
		t.Errorf("Call() error = %q, want %q prefix", err, "http_get:")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("Call() error = %v, want it to wrap errBoom", err)
	}
}

// --- helpers ----------------------------------------------------------------

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Host
}

// bodyOf returns the body portion of an http_get result ("<status>\n<body>").
func bodyOf(result string) string {
	_, body, _ := strings.Cut(result, "\n")
	return body
}
