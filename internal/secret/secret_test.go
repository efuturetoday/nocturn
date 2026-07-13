package secret_test

import (
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

// Host-side injection: the secret is stamped into the outgoing request at the
// border (with prefix), so the request is authenticated without the guest ever
// handling the token.
func TestInject_StampsSecretAtBorder(t *testing.T) {
	s := secret.NewStore()
	s.Set("ms_graph", []byte("abc123"))

	// The guest built this request by NAME only — no credential in sight.
	req := &secret.Request{Method: "GET", URL: "https://graph.microsoft.com/v1.0/me"}

	err := secret.Inject(s, req, secret.Binding{Secret: "ms_graph", Header: "Authorization", Prefix: "Bearer "})
	if err != nil {
		t.Fatalf("inject failed: %v", err)
	}
	if got := req.Headers["Authorization"]; got != "Bearer abc123" {
		t.Fatalf("Authorization header = %q, want %q", got, "Bearer abc123")
	}
}

// Fail closed: injecting a missing secret errors instead of sending an
// unauthenticated request.
func TestInject_MissingSecret_FailsClosed(t *testing.T) {
	s := secret.NewStore()
	req := &secret.Request{Method: "GET", URL: "https://api.example.com"}

	if err := secret.Inject(s, req, secret.Binding{Secret: "absent", Header: "Authorization"}); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, present := req.Headers["Authorization"]; present {
		t.Fatal("no header must be set when the secret is missing")
	}
}
