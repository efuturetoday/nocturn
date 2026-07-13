package secret

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned when a credential is referenced but not in the store,
// so a caller can branch on it (e.g. to prompt the user to set it up).
var ErrNotFound = errors.New("secret not found")

// This file is the credential concern: binding a stored secret to an outgoing
// request and injecting it at the host boundary. When OAuth flow + refresh
// arrive they belong here (or in a dedicated credential package once this
// grows) — the store above stays a plain, kind-agnostic byte store.

// Binding says where a secret goes in an outgoing request: which secret (by
// name) becomes which header, with an optional prefix (e.g. "Bearer ").
type Binding struct {
	Secret string
	Header string
	Prefix string
}

// Request is an outgoing HTTP request the host performs on the guest's behalf.
// The guest builds it WITHOUT credentials; the host injects them at the border.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
}

// Inject stamps the bound secret from s into req at the host boundary. The
// guest never sees the value — it only referenced the secret by name. Returns
// an error if the secret is absent (fail closed: no silent unauthenticated
// request).
func Inject(s *Store, req *Request, b Binding) error {
	value, ok := s.value(b.Secret)
	if !ok {
		return fmt.Errorf("secret %q: %w", b.Secret, ErrNotFound)
	}
	if req.Headers == nil {
		req.Headers = make(map[string]string)
	}
	req.Headers[b.Header] = b.Prefix + string(value)
	return nil
}
