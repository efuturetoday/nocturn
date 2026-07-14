// Package agent owns the session lifecycle — the one place where a conversation,
// its guard, and its epoch live together as a unit.
//
// A "session" used to be implicit (= process lifetime) and split across two
// owners: the message history in brain, the "Allow this session" grants in the
// gateway Guard. This package makes it explicit. A Session bundles:
//
//   - a brain.Conversation (the message history),
//   - a shared gateway.Guard (where session grants are remembered),
//   - a shared capability.EpochRegistry plus the session's own epoch.
//
// Every request runs under the session's epoch (threaded through the context),
// so "Allow this session" grants are bound to that epoch. Reset/Close close the
// epoch — which revokes those grants at once via the registry — and Reset then
// opens a fresh epoch with a fresh conversation. This is the whole point: a
// permission granted for one session cannot linger into the next.
package agent

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
)

// Session is the owner of one interactive session's lifecycle.
type Session struct {
	brain  *brain.Brain
	guard  *gateway.Guard
	epochs *capability.EpochRegistry

	conv  *brain.Conversation
	epoch capability.EpochID
}

// New opens a session on the given brain and guard, sharing the epoch registry
// r (which must be the same registry the guard uses, so grants and revocation
// line up). It opens the first epoch and a fresh conversation.
func New(b *brain.Brain, g *gateway.Guard, r *capability.EpochRegistry) *Session {
	return &Session{
		brain:  b,
		guard:  g,
		epochs: r,
		conv:   b.NewConversation(),
		epoch:  r.Open(),
	}
}

// Ask runs one turn under the session's epoch: it stamps the epoch onto ctx so
// the Guard binds any "Allow this session" grant to it, then drives the
// conversation to a final answer.
func (s *Session) Ask(ctx context.Context, input string) (string, error) {
	ctx = capability.WithEpoch(ctx, s.epoch)
	return s.conv.Send(ctx, input)
}

// Reset ends the current session and starts a new one: it closes the old epoch
// (revoking every "Allow this session" grant bound to it), then opens a fresh
// epoch and a fresh, empty conversation. The next Ask starts clean — no history,
// no lingering approvals.
func (s *Session) Reset() {
	s.epochs.Close(s.epoch)
	s.epoch = s.epochs.Open()
	s.conv = s.brain.NewConversation()
}

// Close ends the session, closing its epoch so its session grants are revoked.
func (s *Session) Close() {
	s.epochs.Close(s.epoch)
}
