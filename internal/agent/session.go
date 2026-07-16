// Package agent owns the session lifecycle — the one place where a conversation,
// its guard, and its permission grants live together as a unit.
//
// A "session" used to be implicit (= process lifetime) and split across two
// owners: the message history in brain, the "Allow this session" grants in the
// gateway Guard. This package makes it explicit. A Session bundles:
//
//   - a brain.Conversation (the message history),
//   - a shared gateway.Guard (the pure authorization composer),
//   - a shared capability.EpochRegistry plus the session's own capability.Grants,
//   - the set of skills loaded into this conversation (for skill.load dedup).
//
// Every request runs under the session's Grants (threaded through ctx), which
// owns the standing decisions: "Allow this session" grants bind to the grants'
// epoch, "Allow always" grants persist. Reset/Close close the epoch (revoking the
// session grants at once) and Reset opens a fresh Grants set — but the persistent
// "always" grants (keyed by the grant-set id) survive, since they are the user's
// standing decision for this workspace. A later workspace layer supplies a
// different grant-set id + ceiling here; nothing else changes.
package agent

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/skill"
)

// Session is the owner of one interactive session's lifecycle.
type Session struct {
	brain  *brain.Brain
	guard  *gateway.Guard
	epochs *capability.EpochRegistry
	store  capability.GrantStore // durable "always" backing; nil = none

	conv   *brain.Conversation
	grants *capability.Grants
	skills *skill.Active // skills loaded into THIS conversation (dedup); reset with it
}

// New opens a session on the given brain and guard, sharing the epoch registry r
// (which must be the guard's registry so grants and revocation line up) and the
// durable grant store (may be nil — "always" then does not persist). It opens the
// first epoch, its grant set, and a fresh conversation.
func New(b *brain.Brain, g *gateway.Guard, r *capability.EpochRegistry, store capability.GrantStore) *Session {
	return &Session{
		brain:  b,
		guard:  g,
		epochs: r,
		store:  store,
		conv:   b.NewConversation(),
		grants: capability.NewGrants(r.Open(), store),
		skills: skill.NewActive(),
	}
}

// Ask runs one turn under the session's grant set: it stamps the grants (and their
// workspace ceiling, if any) onto ctx so the Guard binds standing grants to them
// and enforces the ceiling, then drives the conversation to a final answer.
func (s *Session) Ask(ctx context.Context, input string) (string, error) {
	ctx = capability.WithGrants(ctx, s.grants)
	if s.grants.Ceiling != nil {
		ctx = capability.WithCeiling(ctx, *s.grants.Ceiling)
	}
	ctx = skill.WithActive(ctx, s.skills) // so skill.load deduplicates within this conversation
	return s.conv.Send(ctx, input)
}

// MarkSkill records a skill as already loaded in this conversation — used by the
// explicit /name path, which injects the body itself, so a later model-issued
// skill.load for the same skill is deduplicated instead of re-injecting it.
func (s *Session) MarkSkill(name string) { s.skills.Mark(name) }

// Reset ends the current session and starts a new one: it closes the old epoch
// (revoking every "Allow this session" grant bound to it), opens a fresh epoch +
// grant set and a fresh, empty conversation. Persistent "always" grants (keyed by
// the grant-set id) survive. The next Ask starts clean.
func (s *Session) Reset() {
	s.epochs.Close(s.grants.Epoch)
	s.grants = capability.NewGrants(s.epochs.Open(), s.store)
	s.conv = s.brain.NewConversation()
	s.skills = skill.NewActive() // a fresh conversation has no skills loaded
}

// Close ends the session, closing its epoch so its session grants are revoked.
func (s *Session) Close() {
	s.epochs.Close(s.grants.Epoch)
}
