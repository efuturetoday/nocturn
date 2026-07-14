package secret

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned when a credential is referenced but not in the store,
// so a caller can branch on it (e.g. to prompt the user to set it up).
var ErrNotFound = errors.New("secret not found")

// This file is the credential concern: binding a stored secret to a destination
// host and injecting it, host-side, into outgoing requests bound for that host.
// This is the HttpOnly-cookie model — the guest never names or sees the
// credential; the host attaches it automatically based on where the request is
// going. When OAuth flow + refresh arrive they belong here (or a dedicated
// credential package once this grows); the store stays a plain byte store.

// Binding declares that a stored secret rides along on requests to a host: which
// secret (by name), to which destination Host, into which Header (with an
// optional Prefix like "Bearer ").
//
// Host is a destination pattern matched by hostMatches: an exact host, or a
// "*.suffix" sub-domain pattern. An empty Host (or a bare "*") matches nothing,
// so a credential can never be scoped to every host by omission — the
// cookie-domain rule, fail closed.
//
// Placement is Header-only today. Query/path/body targets are a planned
// extension; the placement is isolated in applyTo so Header+Prefix can later
// become a Target sum type without disturbing Binding or its callers.
type Binding struct {
	Secret string
	Host   string
	Header string
	Prefix string
}

// Request is an outgoing HTTP request the host performs on the guest's behalf.
// The guest builds it WITHOUT credentials; the host injects them at the border.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// Injector is the host-owned credential set — the "cookie jar". It maps a
// destination host to the credential(s) that may ride along to it. The guest
// never references it: like a browser attaching an HttpOnly cookie, the host
// consults it by destination host at the boundary and stamps the secret in.
type Injector struct {
	store    *Store
	bindings []Binding
}

// NewInjector returns an injector over store with the given host bindings.
func NewInjector(store *Store, bindings ...Binding) *Injector {
	return &Injector{store: store, bindings: bindings}
}

// InjectMatching stamps every binding whose Host matches host into req,
// host-side (the guest never saw the value). A binding that matches but whose
// secret is absent from the store is an error (fail closed: no
// half-authenticated request). It returns the names of the injected secrets, so
// a later leak scan can redact them if they echo back. A nil Injector injects
// nothing.
func (in *Injector) InjectMatching(req *Request, host string) ([]string, error) {
	if in == nil {
		return nil, nil
	}
	var injected []string
	for _, b := range in.bindings {
		if !hostMatches(b.Host, host) {
			continue
		}
		value, ok := in.store.value(b.Secret)
		if !ok {
			return nil, fmt.Errorf("credential for %q: secret %q: %w", host, b.Secret, ErrNotFound)
		}
		applyTo(req, b, value)
		injected = append(injected, b.Secret)
	}
	return injected, nil
}

// applyTo places the secret value into req per the binding. This is the single
// placement point: today a header stamp; when query/path/body targets arrive it
// becomes a switch over a Target, leaving Binding's callers untouched.
func applyTo(req *Request, b Binding, value []byte) {
	if req.Headers == nil {
		req.Headers = make(map[string]string)
	}
	req.Headers[b.Header] = b.Prefix + string(value)
}

// hostMatches reports whether a destination host is covered by a binding's Host
// pattern: an exact host, or "*.suffix" (matching sub-domains of suffix, not the
// bare domain). An empty pattern or a bare "*" matches nothing — fail closed, so
// a credential cannot be scoped to every host by omission.
func hostMatches(pattern, host string) bool {
	if host == "" {
		return false
	}
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		return host != suffix && strings.HasSuffix(host, "."+suffix)
	}
	return pattern != "" && pattern != "*" && pattern == host
}
