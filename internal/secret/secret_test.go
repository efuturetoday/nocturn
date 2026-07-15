package secret_test

import (
	"context"
	"errors"
	"testing"

	"github.com/efuturetoday/nocturn/internal/secret"
)

func TestStore_ExistsRevealsPresenceOnly(t *testing.T) {
	s := secret.NewStore()
	if s.Exists("ms_graph") {
		t.Fatal("empty store must not report a secret present")
	}
	s.Set("ms_graph", []byte("super-secret-token"))
	if !s.Exists("ms_graph") {
		t.Fatal("set secret must be reported present")
	}
}

// The guest boundary: a guest is handed a GuestView, through which it can only
// check presence. There is no method to read a value — the type system is the
// guarantee, so a compromised guest cannot exfiltrate the credential.
func TestGuestView_CannotReadValue(t *testing.T) {
	s := secret.NewStore()
	s.Set("ms_graph", []byte("super-secret-token"))

	var guest secret.GuestView = s // this is all the guest ever gets

	if !guest.Exists("ms_graph") {
		t.Fatal("guest view must reveal presence")
	}
	// There is intentionally no guest.Get(...) — presence is the only read.
}

// Host-owned, capability+host-scoped injection: the secret is stamped into the
// outgoing request at the border (with prefix) ONLY when BOTH the capability and
// the destination host match — the guest never handled the token or chose it.
func TestInjector_StampsMatchingAtBorder(t *testing.T) {
	s := secret.NewStore()
	s.Set("ms_graph", []byte("abc123"))
	in := secret.NewInjector(s, secret.Binding{
		Secret: "ms_graph", Capability: "http.read", Host: "graph.microsoft.com", Header: "Authorization", Prefix: "Bearer ",
	})

	// The guest built this request by URL only — no credential in sight.
	req := &secret.Request{Method: "GET", URL: "https://graph.microsoft.com/v1.0/me"}
	names, err := in.InjectMatching(context.Background(), req, "http.read", "graph.microsoft.com")
	if err != nil {
		t.Fatalf("inject failed: %v", err)
	}
	if got := req.Headers["Authorization"]; got != "Bearer abc123" {
		t.Fatalf("Authorization header = %q, want %q", got, "Bearer abc123")
	}
	if len(names) != 1 || names[0] != "ms_graph" {
		t.Fatalf("injected names = %v, want [ms_graph]", names)
	}
}

// A destination that does not match the binding's host gets NO credential — the
// cookie-domain rule.
func TestInjector_NonMatchingHost_NoInjection(t *testing.T) {
	s := secret.NewStore()
	s.Set("ms_graph", []byte("abc123"))
	in := secret.NewInjector(s, secret.Binding{
		Secret: "ms_graph", Capability: "*", Host: "graph.microsoft.com", Header: "Authorization", Prefix: "Bearer ",
	})

	req := &secret.Request{URL: "https://evil.example.com/"}
	names, err := in.InjectMatching(context.Background(), req, "http.read", "evil.example.com")
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if _, present := req.Headers["Authorization"]; present {
		t.Fatal("no credential must be injected for a non-matching host")
	}
	if names != nil {
		t.Fatalf("injected = %v, want none", names)
	}
}

// A binding scoped to one capability does not ride on another, even to the right
// host: a read-only token must not be sent on a write.
func TestInjector_WrongCapability_NoInjection(t *testing.T) {
	s := secret.NewStore()
	s.Set("ms_graph", []byte("abc123"))
	in := secret.NewInjector(s, secret.Binding{
		Secret: "ms_graph", Capability: "http.read", Host: "graph.microsoft.com", Header: "Authorization", Prefix: "Bearer ",
	})

	req := &secret.Request{}
	names, err := in.InjectMatching(context.Background(), req, "http.write", "graph.microsoft.com")
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if _, present := req.Headers["Authorization"]; present {
		t.Fatal("a credential bound to http.read must not ride on an http.write")
	}
	if names != nil {
		t.Fatalf("injected = %v, want none", names)
	}
}

// A "*.suffix" host binding matches sub-domains but not the bare domain.
func TestInjector_WildcardSuffix(t *testing.T) {
	s := secret.NewStore()
	s.Set("k", []byte("v"))
	in := secret.NewInjector(s, secret.Binding{Secret: "k", Capability: "*", Host: "*.example.com", Header: "X-Token"})

	sub := &secret.Request{}
	if _, err := in.InjectMatching(context.Background(), sub, "http.read", "a.example.com"); err != nil || sub.Headers["X-Token"] != "v" {
		t.Fatalf("sub-domain should match: err=%v header=%q", err, sub.Headers["X-Token"])
	}
	bare := &secret.Request{}
	if _, err := in.InjectMatching(context.Background(), bare, "http.read", "example.com"); err != nil {
		t.Fatalf("bare: %v", err)
	}
	if _, present := bare.Headers["X-Token"]; present {
		t.Fatal("the bare domain must not match *.example.com")
	}
}

// Fail closed: a binding that matches but whose secret is missing errors instead
// of sending an unauthenticated request.
func TestInjector_MissingSecret_FailsClosed(t *testing.T) {
	s := secret.NewStore()
	in := secret.NewInjector(s, secret.Binding{Secret: "absent", Capability: "*", Host: "api.example.com", Header: "Authorization"})

	req := &secret.Request{URL: "https://api.example.com"}
	if _, err := in.InjectMatching(context.Background(), req, "http.read", "api.example.com"); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, present := req.Headers["Authorization"]; present {
		t.Fatal("no header must be set when the bound secret is missing")
	}
}

// A dynamic Source is consulted at injection time (not read once): two calls
// through a rotating source stamp different values, proving injection is no
// longer a static store read. This is the seam OAuth refresh rides on.
type rotatingSource struct {
	vals []string
	i    int
}

func (s *rotatingSource) Value(context.Context) ([]byte, error) {
	v := s.vals[s.i%len(s.vals)]
	s.i++
	return []byte(v), nil
}

func TestInjector_DynamicSourceConsulted(t *testing.T) {
	b := secret.Binding{Secret: "tok", Capability: "*", Host: "api.example.com", Header: "X-Token"}
	in := secret.NewInjector(secret.NewStore(), b)
	in.SetSource("tok", &rotatingSource{vals: []string{"one", "two"}})

	req1 := &secret.Request{URL: "https://api.example.com"}
	if _, err := in.InjectMatching(context.Background(), req1, "http.read", "api.example.com"); err != nil {
		t.Fatalf("inject 1: %v", err)
	}
	req2 := &secret.Request{URL: "https://api.example.com"}
	if _, err := in.InjectMatching(context.Background(), req2, "http.read", "api.example.com"); err != nil {
		t.Fatalf("inject 2: %v", err)
	}
	if req1.Headers["X-Token"] != "one" || req2.Headers["X-Token"] != "two" {
		t.Fatalf("dynamic source not consulted per call: %q then %q", req1.Headers["X-Token"], req2.Headers["X-Token"])
	}
}

// A Source that errors is fail-closed: the request gets no header and the error
// propagates (a refresh failure must never send a half-authenticated request).
type errSource struct{ err error }

func (s errSource) Value(context.Context) ([]byte, error) { return nil, s.err }

func TestInjector_SourceError_FailsClosed(t *testing.T) {
	b := secret.Binding{Secret: "tok", Capability: "*", Host: "api.example.com", Header: "Authorization"}
	in := secret.NewInjector(secret.NewStore(), b)
	in.SetSource("tok", errSource{err: errors.New("refresh boom")})

	req := &secret.Request{URL: "https://api.example.com"}
	if _, err := in.InjectMatching(context.Background(), req, "http.read", "api.example.com"); err == nil {
		t.Fatal("expected the source error to propagate")
	}
	if _, present := req.Headers["Authorization"]; present {
		t.Fatal("no header must be set when the source errors")
	}
}
