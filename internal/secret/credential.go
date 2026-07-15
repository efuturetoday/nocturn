package secret

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// Binding declares that a stored secret rides along on a request: which secret
// (by name), for which Capability, to which destination Host, into which Header
// (with an optional Prefix like "Bearer ").
//
// Capability and Host both scope the credential (least privilege — both must
// match). Capability is matched by capMatches: an exact capability name
// ("net.write"), "*" (any capability), or "" (nothing — fail closed). A normal
// token uses Capability "*" (host-scoped like a cookie); tighten to a specific
// capability for e.g. a read-only token. Host is matched by hostMatches: an
// exact host or "*.suffix"; empty/"*" matches nothing (cookie-domain rule).
//
// Placement is Header-only today. Query/path/body targets are a planned
// extension; the placement is isolated in applyTo so Header+Prefix can later
// become a Target sum type without disturbing Binding or its callers.
type Binding struct {
	Secret     string
	Capability string
	Host       string
	Header     string
	Prefix     string
}

// Request is an outgoing HTTP request the host performs on the guest's behalf.
// The guest builds it WITHOUT credentials; the host injects them at the border.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// Source yields a credential's current value at injection time. A plain vault
// secret and a refreshing OAuth token are both Sources; the Injector never
// distinguishes them. Value may perform I/O (a token refresh) and must be
// concurrency-safe; ctx bounds any such I/O.
type Source interface {
	Value(ctx context.Context) ([]byte, error)
}

// storeSource is the default Source: a static read from the byte store. It
// preserves the pre-OAuth behavior (and ErrNotFound) for plain vault secrets.
type storeSource struct {
	store *Store
	name  string
}

func (s storeSource) Value(context.Context) ([]byte, error) {
	v, ok := s.store.value(s.name)
	if !ok {
		return nil, fmt.Errorf("secret %q: %w", s.name, ErrNotFound)
	}
	return v, nil
}

// ownedBinding tags a binding with the owner that installed it (a plugin name,
// or "" for host defaults), so a plugin's bindings can be dropped on uninstall.
type ownedBinding struct {
	owner string
	Binding
}

// Injector is the host-owned credential set — the "cookie jar". It maps a
// destination host to the credential(s) that may ride along to it. The guest
// never references it: like a browser attaching an HttpOnly cookie, the host
// consults it by destination host at the boundary and stamps the secret in.
// Each binding's secret is resolved through a Source; by default a static
// store-backed one, but SetSource can swap in a dynamic (e.g. OAuth) source.
// Bindings and sources are mutated at runtime (plugins add/remove them), so the
// Injector is concurrency-safe.
type Injector struct {
	mu       sync.Mutex
	store    *Store
	sources  map[string]Source // secret name -> source; seeded from the store
	bindings []ownedBinding
}

// NewInjector returns an injector over store with the given host bindings (owner
// ""). Every referenced secret is seeded with a static store-backed Source; use
// SetSource to override one with a dynamic (refreshing) source.
func NewInjector(store *Store, bindings ...Binding) *Injector {
	in := &Injector{store: store, sources: make(map[string]Source)}
	for _, b := range bindings {
		in.addBindingLocked("", b)
	}
	return in
}

func (in *Injector) addBindingLocked(owner string, b Binding) {
	in.bindings = append(in.bindings, ownedBinding{owner: owner, Binding: b})
	if _, ok := in.sources[b.Secret]; !ok {
		in.sources[b.Secret] = storeSource{store: in.store, name: b.Secret}
	}
}

// AddBinding installs a binding tagged with owner (a plugin name), seeding a
// static store-backed Source for its secret if none exists yet.
func (in *Injector) AddBinding(owner string, b Binding) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.addBindingLocked(owner, b)
}

// RemoveBindingsFor drops every binding installed by owner (plugin uninstall).
// Sources are left in place (a dropped binding already stops injection; a shared
// source must not vanish under another owner).
func (in *Injector) RemoveBindingsFor(owner string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	kept := in.bindings[:0]
	for _, b := range in.bindings {
		if b.owner != owner {
			kept = append(kept, b)
		}
	}
	in.bindings = kept
}

// SetSource overrides the Source for a secret name with a dynamic one (e.g. an
// OAuth token source that refreshes).
func (in *Injector) SetSource(name string, src Source) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.sources[name] = src
}

// InjectMatching stamps every binding matching both the capability and the
// destination host into req, host-side (the guest never saw the value). Each
// value comes from the binding's Source (a static store read, or a refreshing
// OAuth token). A missing source or any Source error is fail-closed: no
// half-authenticated request leaves. It returns the names of the injected
// secrets, so a later leak scan can redact them if they echo back. A nil
// Injector injects nothing.
func (in *Injector) InjectMatching(ctx context.Context, req *Request, capability, host string) ([]string, error) {
	if in == nil {
		return nil, nil
	}
	// Snapshot the matching (binding, source) pairs under the lock; resolve the
	// values OUTSIDE it — Source.Value may refresh (I/O) and must not hold the lock.
	type match struct {
		b   Binding
		src Source
	}
	var matches []match
	in.mu.Lock()
	for _, ob := range in.bindings {
		if !capMatches(ob.Capability, capability) || !hostMatches(ob.Host, host) {
			continue
		}
		src, ok := in.sources[ob.Secret]
		if !ok { // a binding with no registered source is fail-closed
			in.mu.Unlock()
			return nil, fmt.Errorf("credential %q for %s: %w", ob.Secret, host, ErrNotFound)
		}
		matches = append(matches, match{b: ob.Binding, src: src})
	}
	in.mu.Unlock()

	var injected []string
	for _, m := range matches {
		value, err := m.src.Value(ctx)
		if err != nil { // any source error aborts: no half-authenticated request
			return nil, fmt.Errorf("credential %q for %s: %w", m.b.Secret, host, err)
		}
		applyTo(req, m.b, value)
		injected = append(injected, m.b.Secret)
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

// capMatches reports whether a call's capability is covered by a binding's
// Capability pattern: an exact name, "*" (any capability), or "" (nothing —
// fail closed, so a forgotten field never grants to every capability).
func capMatches(pattern, capability string) bool {
	switch pattern {
	case "":
		return false
	case "*":
		return true
	default:
		return pattern == capability
	}
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
