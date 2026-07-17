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

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// A *Session drives a Runner (it satisfies the turns interface: Ask/Reset/History) —
// asserted here because production wires them together but the tests use a fake.
var _ turns = (*Session)(nil)

// Session is the owner of one interactive session's lifecycle. It holds everything a
// chat needs: the toolset it may use (tools) and the loaded skills (both handed to /
// stamped for each turn) alongside the conversation and permission scope. The Brain
// is the stateless executor it drives — it owns none of this.
type Session struct {
	brain   *brain.Brain
	guard   *gateway.Guard
	tools   *tool.Registry        // the toolset this session may use (passed to the brain per turn)
	store   capability.GrantStore // durable "always" backing; nil = none
	persona string                // the session's system prompt, re-seeded on every fresh conversation
	history []brain.Message       // prior turns to seed a REOPENED chat (used once, at New; not on Reset)

	conv   *brain.Conversation
	scope  *gateway.Scope
	skills *skill.Active // skills loaded into THIS conversation (dedup); reset with it
}

// Option configures a Session built with New.
type Option func(*Session)

// WithPersona sets the session's system prompt — its standing identity, seeded on every
// conversation (including after Reset). Optional: omit it (or pass "") for a session with
// no persona. The workspace supplies the resolved PERSONA.md here.
func WithPersona(persona string) Option {
	return func(s *Session) { s.persona = persona }
}

// WithHistory seeds a REOPENED chat with its saved turns (after the persona). Used once, at
// New; Reset deliberately starts empty (a cleared chat), so history is not re-seeded there.
func WithHistory(msgs []brain.Message) Option {
	return func(s *Session) { s.history = msgs }
}

// New opens a session on the given brain over tools (the session's toolset), with a
// durable grant store (may be nil — "always" then does not persist). Options set the
// persona (WithPersona). It opens the first Scope (a fresh epoch + grant set on the
// guard's registry) and a fresh conversation over tools.
func New(b *brain.Brain, tools *tool.Registry, g *gateway.Guard, store capability.GrantStore, opts ...Option) *Session {
	s := &Session{
		brain:  b,
		guard:  g,
		tools:  tools,
		store:  store,
		scope:  g.NewScope(store),
		skills: skill.NewActive(),
	}
	for _, o := range opts {
		o(s)
	}
	s.conv = b.NewConversation(tools, brain.WithSystem(s.persona), brain.WithHistory(s.history))
	return s
}

// Ask runs one turn as the workspace's ROOT agent — the empty agent.Agent{} (no
// restrictions, the session's full tools via s.conv), over its PERSISTENT scope and
// conversation. It is the exact same execution as a child agent.Run, differing only in
// its inputs (persistent memory + full tools + empty config vs. fresh + filtered +
// declared), so it goes through the one shared agent.Turn — no duplicated ceremony.
func (s *Session) Ask(ctx context.Context, input string) (string, error) {
	return agent.Turn(ctx, s.scope, s.skills, agent.Agent{}, s.conv, input)
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
	s.conv = s.brain.NewConversation(s.tools, brain.WithSystem(s.persona))
	s.skills = skill.NewActive() // a fresh conversation has no skills loaded
}

// Close ends the session, revoking its scope so its session grants are revoked.
func (s *Session) Close() {
	s.scope.Revoke()
}
