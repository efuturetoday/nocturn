package capability

import (
	"context"
	"sync"
)

// Scope is how long a user-granted "allow" lasts.
type Scope int

const (
	// Once allows a single call and records nothing — the next matching call asks again.
	Once Scope = iota
	// Session allows until the context's epoch closes (revoked on Reset/Close).
	Session
	// Always persists across restarts (stored in the context's durable grant set).
	Always
)

// PersistentGrants is a context's durable "always" permission set. It is an
// interface so the concrete store (a file) lives in an outer layer and this
// package stays pure decision logic — no I/O. Consulted/updated by Context,
// keyed by context id.
type PersistentGrants interface {
	Allows(contextID string, call Call) bool
	Record(contextID string, call Call) error
}

// Context owns a permission set: the user's standing decisions for a session
// (today) or a workspace (later). It holds session-scoped grants (bound to its
// epoch — closing the epoch revokes them) and, via an injected store, always-
// scoped grants that persist. It may also carry a workspace-level Ceiling. The
// Guard consults the active Context (carried in ctx) to see whether an Ask is
// already answered by a standing grant. A later workspace layer supplies a
// different id + a persistent store — no other change.
type Context struct {
	ID      string
	Epoch   EpochID          // session grants bind here; closing it revokes them
	Ceiling *Ceiling         // optional workspace-level upper bound (nil = none)
	always  PersistentGrants // durable always-grants keyed by ID; nil = none

	mu      sync.Mutex
	session []Rule
}

// NewContext builds a context. always may be nil (no durable grants — e.g. tests).
func NewContext(id string, epoch EpochID, always PersistentGrants) *Context {
	return &Context{ID: id, Epoch: epoch, always: always}
}

// Allows reports whether call is covered by a live standing grant — a session
// grant (bound to a still-alive epoch, via env) or a persisted always grant.
func (c *Context) Allows(call Call, env Env) bool {
	c.mu.Lock()
	sess := Policy{Rules: append([]Rule(nil), c.session...)}
	c.mu.Unlock()
	if sess.Evaluate(call, env) == Allow {
		return true
	}
	return c.always != nil && c.always.Allows(c.ID, call)
}

// Record stores a user's grant at scope. Once records nothing (the HITL outcome
// alone allows the one call). Session appends an epoch-bound Allow rule. Always
// persists through the durable store (a no-op if none is wired).
func (c *Context) Record(call Call, scope Scope) error {
	switch scope {
	case Session:
		c.mu.Lock()
		c.session = append(c.session, Rule{
			Capability: call.Capability,
			HostGlob:   call.Attrs["host"], // exact host; "" matches hostless calls
			Effect:     Allow,
			Epoch:      c.Epoch,
		})
		c.mu.Unlock()
	case Always:
		if c.always != nil {
			return c.always.Record(c.ID, call)
		}
	}
	return nil
}

// The active Context travels through the request context so the Guard reads it
// without widening every signature — same idiom as the epoch.

type contextKey struct{}

// WithContext returns a ctx carrying c as the active permission context.
func WithContext(ctx context.Context, c *Context) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// ContextFrom returns the active Context, or nil if none (a caller with no
// standing-grant home — every effect then asks).
func ContextFrom(ctx context.Context) *Context {
	c, _ := ctx.Value(contextKey{}).(*Context)
	return c
}
