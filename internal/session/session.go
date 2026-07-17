// Package session is one interactive chat's lifecycle and orchestration.
//
// A Session is the STATE unit: it bundles a conversation, its guard, its revocable
// permission scope, and its loaded skills as one thing that resets and closes
// together:
//
//   - a brain.Conversation (the message history),
//   - a shared gateway.Guard (the pure authorization composer),
//   - a gateway.Scope (the revocable epoch + standing grants; the Guard owns the
//     epoch mechanism, so the session never touches the EpochRegistry directly),
//   - the set of skills loaded into this conversation (for skill.load dedup).
//
// Every request runs under the session's Scope (threaded through ctx via Bind),
// which owns the standing decisions: "Allow this session" grants bind to the scope's
// epoch, "Allow always" grants persist. Reset/Close revoke the scope (revoking the
// session grants at once) and Reset opens a fresh Scope — but the persistent "always"
// grants (in the durable store) survive.
//
// A Runner is the ORCHESTRATION unit over a Session: COMMANDS in (Submit/Cancel/
// Reset/Resolve), EVENTS out (Subscribe), one turn at a time over a buffered input
// queue — the headless heart a TUI, a REST/SSE server, or a mobile app all drive the
// same way. It carries no transport and no approval-mechanism types; approvals
// surface as events enacted through an opaque callback (see ApprovalSink).
package session

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/skill"
)

// A *Session drives a Runner (it satisfies the turns interface: Ask/Reset/History) —
// asserted here because production wires them together but the tests use a fake.
var _ turns = (*Session)(nil)

// Session is the owner of one interactive session's lifecycle.
type Session struct {
	brain *brain.Brain
	guard *gateway.Guard
	store capability.GrantStore // durable "always" backing; nil = none

	conv   *brain.Conversation
	scope  *gateway.Scope
	skills *skill.Active // skills loaded into THIS conversation (dedup); reset with it
}

// New opens a session on the given brain and guard, with a durable grant store (may
// be nil — "always" then does not persist). It opens the first Scope (a fresh epoch +
// grant set on the guard's registry) and a fresh conversation.
func New(b *brain.Brain, g *gateway.Guard, store capability.GrantStore) *Session {
	return &Session{
		brain:  b,
		guard:  g,
		store:  store,
		conv:   b.NewConversation(),
		scope:  g.NewScope(store),
		skills: skill.NewActive(),
	}
}

// Ask runs one turn under the session's scope: Bind stamps the scope's grants (and
// its workspace cage, if any) onto ctx so the Guard binds standing grants to them and
// enforces the cage, then drives the conversation to a final answer.
func (s *Session) Ask(ctx context.Context, input string) (string, error) {
	ctx = s.scope.Bind(ctx)
	ctx = skill.WithActive(ctx, s.skills) // so skill.load deduplicates within this conversation
	return s.conv.Send(ctx, input)
}

// History returns a copy of this session's conversation so far (for a client
// snapshot / reconnect). A snapshot between turns; live tokens stream separately.
func (s *Session) History() []brain.Message { return s.conv.Messages() }

// MarkSkill records a skill as already loaded in this conversation — used by the
// explicit /name path, which injects the body itself, so a later model-issued
// skill.load for the same skill is deduplicated instead of re-injecting it.
func (s *Session) MarkSkill(name string) { s.skills.Mark(name) }

// Reset ends the current session and starts a new one: it revokes the old scope
// (revoking every "Allow this session" grant bound to its epoch), opens a fresh scope
// and a fresh, empty conversation. Persistent "always" grants (in the durable store)
// survive. The next Ask starts clean.
func (s *Session) Reset() {
	s.scope.Revoke()
	s.scope = s.guard.NewScope(s.store)
	s.conv = s.brain.NewConversation()
	s.skills = skill.NewActive() // a fresh conversation has no skills loaded
}

// Close ends the session, revoking its scope so its session grants are revoked.
func (s *Session) Close() {
	s.scope.Revoke()
}
