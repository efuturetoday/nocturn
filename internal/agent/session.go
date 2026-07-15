// Package agent owns the session lifecycle — the one place where a conversation,
// its guard, and its permission context live together as a unit.
//
// A "session" used to be implicit (= process lifetime) and split across two
// owners: the message history in brain, the "Allow this session" grants in the
// gateway Guard. This package makes it explicit. A Session bundles:
//
//   - a brain.Conversation (the message history),
//   - a shared gateway.Guard (the pure authorization composer),
//   - a shared capability.EpochRegistry plus the session's own capability.Context.
//
// Every request runs under the session's Context (threaded through ctx), which
// owns the standing grants: "Allow this session" grants bind to the context's
// epoch, "Allow always" grants persist. Reset/Close close the epoch (revoking the
// session grants at once) and Reset opens a fresh Context — but the persistent
// "always" grants (keyed by the context id) survive, since they are the user's
// standing decision for this workspace. A later workspace layer supplies a
// different context id + ceiling here; nothing else changes.
package agent

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
)

// contextID is the permission-context id for the default (single) session. A
// future workspace layer supplies distinct ids here.
const contextID = "default"

// Session is the owner of one interactive session's lifecycle.
type Session struct {
	brain  *brain.Brain
	guard  *gateway.Guard
	epochs *capability.EpochRegistry
	grants capability.PersistentGrants // durable "always" store; nil = none

	conv *brain.Conversation
	pctx *capability.Context
}

// New opens a session on the given brain and guard, sharing the epoch registry r
// (which must be the guard's registry so grants and revocation line up) and the
// durable grant store (may be nil — "always" then does not persist). It opens the
// first epoch, its permission context, and a fresh conversation.
func New(b *brain.Brain, g *gateway.Guard, r *capability.EpochRegistry, grants capability.PersistentGrants) *Session {
	return &Session{
		brain:  b,
		guard:  g,
		epochs: r,
		grants: grants,
		conv:   b.NewConversation(),
		pctx:   capability.NewContext(contextID, r.Open(), grants),
	}
}

// Ask runs one turn under the session's permission context: it stamps the context
// (and its workspace ceiling, if any) onto ctx so the Guard binds standing grants
// to it and enforces the ceiling, then drives the conversation to a final answer.
func (s *Session) Ask(ctx context.Context, input string) (string, error) {
	ctx = capability.WithContext(ctx, s.pctx)
	if s.pctx.Ceiling != nil {
		ctx = capability.WithCeiling(ctx, *s.pctx.Ceiling)
	}
	return s.conv.Send(ctx, input)
}

// Reset ends the current session and starts a new one: it closes the old epoch
// (revoking every "Allow this session" grant bound to it), opens a fresh epoch +
// permission context and a fresh, empty conversation. Persistent "always" grants
// (keyed by the context id) survive. The next Ask starts clean.
func (s *Session) Reset() {
	s.epochs.Close(s.pctx.Epoch)
	s.pctx = capability.NewContext(contextID, s.epochs.Open(), s.grants)
	s.conv = s.brain.NewConversation()
}

// Close ends the session, closing its epoch so its session grants are revoked.
func (s *Session) Close() {
	s.epochs.Close(s.pctx.Epoch)
}
