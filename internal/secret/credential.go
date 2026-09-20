package secret

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
// (by name), to which destination Host, into which Header (with an optional
// Prefix like "Bearer ").
//
// Host is the sole scoping dimension — a request credential is inherently a
// network credential, so a separate "kind" would be redundant; the host IS the
// discriminator. It is matched by hostMatches: an exact host or "*.suffix";
// empty/"*" matches nothing (cookie-domain rule, fail closed). Owner scoping
// (via WithOwner) is the second boundary, so one plugin never rides another's
// token even at a shared host.
//
// Placement is Header-only today. Query/path/body targets are a planned
// extension; the placement is isolated in applyTo so Header+Prefix can later
// become a Target sum type without disturbing Binding or its callers.
type Binding struct {
	Secret string
	Host   string
	Header string
	Prefix string

	// Ambient makes a binding ride ANY caller's request to its host, including the model's own
	// http_read, while still belonging to the owner it was registered under.
	//
	// It exists because owner scoping assumes the owner EXECUTES. A plugin's guest runs with its
	// owner on the context, so its token can be confined to its own calls; a skill has no runtime at
	// all — it is text, and the model is what acts on it — so there is no execution context to
	// attribute the request to, and a skill credential confined to its owner would never be injected
	// by anything. The owner still governs identity and lifetime: the key names the skill, removing
	// the skill removes the binding. What it cannot do for an ambient binding is separate two skills
	// that talk to the same host, because at the boundary those two requests are indistinguishable.
	Ambient bool
}

// Request is an outgoing HTTP request the host performs on the guest's behalf.
// The guest builds it WITHOUT credentials; the host injects them at the border.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// Resolver yields a credential's current value at injection time. A plain vault
// secret and a refreshing OAuth token are both Sources; the Injector never
// distinguishes them. Value may perform I/O (a token refresh) and must be
// concurrency-safe; ctx bounds any such I/O.
type Resolver interface {
	Value(ctx context.Context) ([]byte, error)
}

// storeResolver is the default Resolver: a static read from the byte store. It
// preserves the pre-OAuth behavior (and ErrNotFound) for plain vault secrets.
type storeResolver struct {
	store *Store
	name  string
}

func (s storeResolver) Value(context.Context) ([]byte, error) {
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
// Each binding's secret is resolved through a Resolver; by default a static
// store-backed one, but SetResolver can swap in a dynamic (e.g. OAuth) source.
// Bindings and resolvers are mutated at runtime (plugins add/remove them), so the
// Injector is concurrency-safe.
type Injector struct {
	mu        sync.Mutex
	store     *Store
	resolvers map[string]Resolver // secret name -> source; seeded from the store
	bindings  []ownedBinding
	log       *slog.Logger // traces injection + fail-closed branches; nil = silent (never logs a value)
}

// SetLogger attaches a logger for injection tracing: a successful injection (Debug) and the two
// fail-closed branches — a binding with no source, and a Resolver error — at Warn. The caller passes
// an already-tagged logger (component=secret, ws); only the secret NAME and host are logged, never
// the value. nil disables it.
func (in *Injector) SetLogger(l *slog.Logger) {
	if l == nil {
		return // keep the no-op default
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.log = l
}

// NewInjector returns an injector over store with the given host bindings (owner
// ""). Every referenced secret is seeded with a static store-backed Resolver; use
// SetResolver to override one with a dynamic (refreshing) source.
func NewInjector(store *Store, bindings ...Binding) *Injector {
	in := &Injector{store: store, resolvers: make(map[string]Resolver), log: slog.New(slog.DiscardHandler)}
	for _, b := range bindings {
		in.addBindingLocked("", b)
	}
	return in
}

func (in *Injector) addBindingLocked(owner string, b Binding) {
	in.bindings = append(in.bindings, ownedBinding{owner: owner, Binding: b})
	if _, ok := in.resolvers[b.Secret]; !ok {
		in.resolvers[b.Secret] = storeResolver{store: in.store, name: b.Secret}
	}
}

// SetOwned replaces EVERY owned binding with the given set, in one step under one lock. Unowned
// bindings (app defaults, handed to NewInjector) are untouched.
//
// One call rather than add/remove pairs, because both hazards this replaced were shapes of "the
// injector is durable while a discovery pass is not". Adding without clearing injected a credential
// once more per reload; clearing from a list kept beside the last published snapshot missed the
// bindings of a pass that failed, so they outlived the extension that declared them. And doing it in
// two steps left a window in which an in-flight request found no binding at all and went out
// unauthenticated. A single swap has none of the three, by construction rather than by comment.
//
// Resolvers are preserved for every secret still bound afterwards. That is not an optimisation: an
// OAuth credential's resolver REFRESHES the token, and it is registered by a different part of the
// pass. Dropping and re-seeding it here would replace it with a static store read, which for an OAuth
// credential means the stored token JSON — the whole serialized token, refresh token included — would
// be stamped into an Authorization header. A resolver whose secret is no longer bound by anything is
// dropped, so an uninstall still forgets the credential material it referenced.
func (in *Injector) SetOwned(owned map[string][]Binding) {
	in.mu.Lock()
	defer in.mu.Unlock()

	kept := in.bindings[:0]
	for _, b := range in.bindings {
		if b.owner == "" {
			kept = append(kept, b)
		}
	}
	in.bindings = kept
	for owner, bindings := range owned {
		for _, b := range bindings {
			in.bindings = append(in.bindings, ownedBinding{owner: owner, Binding: b})
		}
	}

	// Seed a static resolver for anything newly bound that has none, and forget the resolvers of
	// secrets nothing binds any more.
	bound := make(map[string]bool, len(in.bindings))
	for _, b := range in.bindings {
		bound[b.Secret] = true
		if _, ok := in.resolvers[b.Secret]; !ok {
			in.resolvers[b.Secret] = storeResolver{store: in.store, name: b.Secret}
		}
	}
	for name := range in.resolvers {
		if !bound[name] {
			delete(in.resolvers, name)
		}
	}
}

// SetResolver overrides the Resolver for a secret name with a dynamic one (e.g. an
// OAuth token source that refreshes).
func (in *Injector) SetResolver(name string, src Resolver) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.resolvers[name] = src
}

// InjectMatching stamps every binding matching the destination host AND the
// calling owner into req, host-side (the guest never saw the value). Owner
// scoping (via WithOwner on ctx) keeps a plugin's credential on
// its OWN calls only: a binding rides along iff it is unowned (an app default) or
// owned by the calling plugin — so plugin B can never pick up plugin A's token,
// even when both declare the same host. Each value comes from the binding's
// Resolver (a static store read, or a refreshing OAuth token). A missing source or
// any Resolver error is fail-closed: no half-authenticated request leaves. It
// returns the names of the injected secrets, so a later leak scan can redact them
// if they echo back. A nil Injector injects nothing.
func (in *Injector) InjectMatching(ctx context.Context, req *Request, host string) ([]string, error) {
	if in == nil {
		return nil, nil
	}
	caller := ownerFrom(ctx)
	// Snapshot the matching (binding, source) pairs under the lock; resolve the
	// values OUTSIDE it — Resolver.Value may refresh (I/O) and must not hold the lock.
	type match struct {
		b   Binding
		src Resolver
	}
	var matches []match
	in.mu.Lock()
	lg := in.log // snapshot under the lock; used unlocked below
	if lg == nil {
		lg = slog.New(slog.DiscardHandler) // a directly-constructed Injector may not have set one
	}
	for _, ob := range in.bindings {
		if !ownerMatches(ob, caller) || !hostMatches(ob.Host, host) {
			continue
		}
		src, ok := in.resolvers[ob.Secret]
		if !ok { // a binding with no registered source is fail-closed
			in.mu.Unlock()
			lg.Warn("credential unavailable — request goes out unauthenticated", "secret", ob.Secret, "host", host)
			return nil, fmt.Errorf("credential %q for %s: %w", ob.Secret, host, ErrNotFound)
		}
		matches = append(matches, match{b: ob.Binding, src: src})
	}
	in.mu.Unlock()

	var injected []string
	for _, m := range matches {
		value, err := m.src.Value(ctx)
		if err != nil { // any source error aborts: no half-authenticated request
			lg.Warn("credential resolver failed — request aborted", "secret", m.b.Secret, "host", host, "err", err)
			return nil, fmt.Errorf("credential %q for %s: %w", m.b.Secret, host, err)
		}
		applyTo(req, m.b, value)
		injected = append(injected, m.b.Secret)
		lg.Debug("credential injected", "secret", m.b.Secret, "host", host)
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

// ownerKey carries the identity of the plugin whose sandbox issued the current
// call, so credential injection stays plugin-scoped.
type ownerKey struct{}

// WithOwner marks ctx as originating from a specific plugin (its manifest name).
// The plugin layer stamps it before running a plugin's guest, so a credential
// injected on requests from that guest is limited to the plugin's own bindings
// (plus the app's unowned defaults). Calls without it — the model's own tool
// calls, or a script's — see only the unowned defaults.
func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerKey{}, owner)
}

func ownerFrom(ctx context.Context) string {
	o, _ := ctx.Value(ownerKey{}).(string)
	return o
}

// ownerMatches reports whether ob may ride along on a call from caller: an unowned
// binding (owner "") is an app default shared by all callers; an owned binding rides
// ONLY on its owner's own calls, unless it is Ambient — see Binding.Ambient for why a
// skill's credential has to be.
//
// It takes the owned binding whole rather than the flag and the owner side by side,
// because those two are one fact and passing them separately is a way to ask about one
// binding's ambience and another's owner.
func ownerMatches(ob ownedBinding, caller string) bool {
	// Ambient means the UNOWNED caller — the model's own tool call — not "everybody". A plugin guest
	// and an MCP connection both carry their owner on the context and make their own requests; letting
	// a skill's credential ride those too would hand every installed plugin the household's token for
	// that host, which is strictly more than owner scoping gave anyone before.
	return (ob.Ambient && caller == "") || ob.owner == "" || ob.owner == caller
}

// hostMatches reports whether a destination host is covered by a binding's Host
// pattern: an exact host, or "*.suffix" (matching sub-domains of suffix, not the
// bare domain). An empty pattern or a bare "*" matches nothing — fail closed, so
// a credential cannot be scoped to every host by omission.
func hostMatches(pattern, host string) bool {
	if host == "" {
		return false
	}
	// Case-INSENSITIVE, and in this one place. Hostnames are case-insensitive, but the two sides of
	// this comparison come from different worlds: the pattern from a declaration somebody wrote, the
	// host from whatever URL the caller built. Lowercasing only one side (which is what a
	// declaration-side fix does) moves the mismatch rather than removing it, and the symptom is the
	// worst kind — the credential is seedable, listable, and simply never injected, with the request
	// going out unauthenticated and nothing saying why.
	host = strings.ToLower(host)
	pattern = strings.ToLower(pattern)
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		return host != suffix && strings.HasSuffix(host, "."+suffix)
	}
	return pattern != "" && pattern != "*" && pattern == host
}
