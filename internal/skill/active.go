package skill

import (
	"context"
	"sync"
)

// Active is the set of skills already loaded into the current conversation. It
// lives for the session (the lifecycle owner stamps it into ctx and refreshes it
// on reset) so a re-activation can be answered "already loaded" instead of
// injecting the same body twice — the deduplication the standard calls for.
type Active struct {
	mu    sync.Mutex
	names map[string]bool
}

// NewActive returns an empty activation set.
func NewActive() *Active { return &Active{names: map[string]bool{}} }

// Mark records name as loaded and reports whether it was newly added (false = it
// was already active).
func (a *Active) Mark(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.names[name] {
		return false
	}
	a.names[name] = true
	return true
}

// Has reports whether name is already loaded.
func (a *Active) Has(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.names[name]
}

type activeKey struct{}

// WithActive carries the session's activation set through the request context so
// the skill.load tool can deduplicate without a wider signature — same idiom as
// the epoch and the grants.
func WithActive(ctx context.Context, a *Active) context.Context {
	return context.WithValue(ctx, activeKey{}, a)
}

// ActiveFrom returns the activation set, or nil if none (no session context — a
// caller with no dedup home; every activation then re-injects).
func ActiveFrom(ctx context.Context) *Active {
	a, _ := ctx.Value(activeKey{}).(*Active)
	return a
}
