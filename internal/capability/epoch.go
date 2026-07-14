package capability

import (
	"context"
	"sync"
)

// EpochID identifies a task/subgoal an authority is bound to. The zero value
// is "unset" and matches nothing (fail closed) — permanence is never implicit.
// Use Permanent for a grant that never expires, or an id from
// EpochRegistry.Open for a task-scoped grant.
type EpochID uint64

// Permanent marks a grant that is never epoch-scoped and never expires. It must
// be set explicitly on a Rule; the zero value does NOT mean permanent, so a
// forgotten Epoch field fails closed instead of silently granting lasting
// authority. Its value is a reserved sentinel that Open never mints.
const Permanent EpochID = ^EpochID(0)

// EpochRegistry tracks which epochs are alive. A grant bound to an epoch is
// honoured only while that epoch is alive; closing the epoch revokes every
// grant bound to it at once. This kills "lingering authority": a permission
// granted for a task dies when the task ends, so a later (possibly injected)
// instruction cannot reuse it.
type EpochRegistry struct {
	mu    sync.Mutex
	next  EpochID
	alive map[EpochID]bool
}

// NewEpochRegistry returns an empty registry.
func NewEpochRegistry() *EpochRegistry {
	return &EpochRegistry{alive: make(map[EpochID]bool)}
}

// Open mints a new, alive epoch and returns its id (always non-zero).
func (r *EpochRegistry) Open() EpochID {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	r.alive[r.next] = true
	return r.next
}

// Close revokes an epoch. Every grant bound to it stops matching immediately;
// a stale attempt to reuse it is denied before any side effect. Closing an
// unknown or already-closed epoch is a no-op.
func (r *EpochRegistry) Close(id EpochID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.alive, id)
}

// IsAlive reports whether an epoch is currently alive.
func (r *EpochRegistry) IsAlive(id EpochID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.alive[id]
}

// The epoch also travels through a request context, so the epoch a call is bound
// to reaches the Guard without widening every signature down the tool chain
// (Session.Ask → Conversation.Send → brain.run → tool.Invoke → Guard.Authorize).
// Epoch scoping is request-scoped metadata; context is its idiomatic home.

type epochKey struct{}

// WithEpoch returns a context carrying id as the active epoch. The Guard reads
// it (via EpochFrom) to bind "Allow this session" grants to that epoch, so
// closing the epoch revokes them.
func WithEpoch(ctx context.Context, id EpochID) context.Context {
	return context.WithValue(ctx, epochKey{}, id)
}

// EpochFrom returns the active epoch carried by ctx, or the zero EpochID (which
// is "unset" and matches nothing — fail closed) if none is present.
func EpochFrom(ctx context.Context) EpochID {
	id, _ := ctx.Value(epochKey{}).(EpochID)
	return id
}
