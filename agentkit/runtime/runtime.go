// Package runtime wires the pieces — an agentkit LLM, a toolset, skills, permission gating and
// session defaults — into ready-to-run sessions. Build a Runtime once, then start Sessions or
// one-shot Once calls that all share its policy, grants, approver and tools. It is the composition
// root: the plumbing lives here so a consumer configures, not wires.
//
// Share the tools, not a snapshot of them: a Runtime holds the toolset as a function its sessions ask
// at the start of every turn (see WithToolsFunc). A consumer whose available tools change while
// conversations are open therefore answers the next turn with the new set, without reopening
// anything — and a turn still works with one fixed set from beginning to end.
package runtime

import (
	"context"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// Runtime holds the shared configuration for a set of sessions.
type Runtime struct {
	llm      agentkit.LLM
	toolsFn  func() agentkit.ToolSet
	skillsFn func() agentkit.SkillSet
	policy   gate.Policy
	grants   gate.Grants
	approver gate.Approver
	gateLog  agentkit.Logger   // traces gate decisions; nil = no-op
	base     []agentkit.Option // default session options
}

// Option configures a Runtime.
type Option func(*Runtime)

// WithToolsFunc sets the toolset through a function the session evaluates once per turn, for a
// consumer whose available tools change while conversations are open. Gating still applies: the set
// the function returns is wrapped on the way out, so tools that appear later are gated exactly like
// the ones that were there at the start.
func WithToolsFunc(fn func() agentkit.ToolSet) Option { return func(r *Runtime) { r.toolsFn = fn } }

// WithSkillsFunc is WithToolsFunc for the skill catalog.
func WithSkillsFunc(fn func() agentkit.SkillSet) Option {
	return func(r *Runtime) { r.skillsFn = fn }
}

// WithTools sets a fixed toolset. When gating is configured (WithGate), the tools are wrapped so each
// call is gated on its name; a tool that self-checks a runtime target (a host) is unaffected —
// classify its target axis, not its name, so the wrapper's name check just passes.
func WithTools(ts agentkit.ToolSet) Option {
	return WithToolsFunc(func() agentkit.ToolSet { return ts })
}

// WithSkills sets a fixed skill set.
func WithSkills(ss agentkit.SkillSet) Option {
	return WithSkillsFunc(func() agentkit.SkillSet { return ss })
}

// WithGate installs permission gating: a Policy plus a Grants store (nil = in-memory) and an Approver
// (nil = unattended, so any Ask is denied). Without it, tools run ungated.
func WithGate(policy gate.Policy, grants gate.Grants, approver gate.Approver) Option {
	return func(r *Runtime) { r.policy, r.grants, r.approver = policy, grants, approver }
}

// WithGateLogger sets the logger that traces gate decisions (allow/deny/ask/grant). Pass the same
// logger the sessions use so the lines carry ws/chat/component; nil (the default) traces nothing.
func WithGateLogger(log agentkit.Logger) Option {
	return func(r *Runtime) { r.gateLog = log }
}

// WithSession adds default agentkit session options — system prompt, timeout, token limit, tokenizer,
// store — applied when each Session/Once is BUILT.
//
// Applied once is not the same as fixed once, and the difference matters here: an option installs
// whatever the session then consults. WithTimeout and WithStore install values, read as they are for
// the session's life. WithSystem installs a function that returns a constant, and WithSystemFunc one
// that need not — either way the session asks it at the start of every turn, so a consumer folding in
// mutable context is answered by the next turn rather than the next session.
//
// Tools and skills do NOT come through here: they are the Runtime's own, see WithToolsFunc.
func WithSession(opts ...agentkit.Option) Option {
	return func(r *Runtime) { r.base = append(r.base, opts...) }
}

// New builds a Runtime over an LLM. Any adapter satisfying agentkit.LLM works.
func New(llm agentkit.LLM, opts ...Option) *Runtime {
	r := &Runtime{llm: llm}
	for _, o := range opts {
		o(r)
	}
	if r.policy != nil {
		if r.grants == nil {
			r.grants = gate.NewMemGrants()
		}
		// Wrap on the way OUT rather than once here: the set is resolved per turn now, so a tool that
		// only appears on a later turn has to meet the gate too. Wrapping the function's result is what
		// makes that true by construction instead of by anyone remembering.
		if inner := r.toolsFn; inner != nil {
			r.toolsFn = func() agentkit.ToolSet { return gate.WrapAll(inner()) }
		}
	}
	return r
}

// Session starts a live session: the runtime's LLM and gated tools, with the permission machinery
// installed on its ctx (so it reaches every tool call and sub-agent). Extra options override the
// runtime defaults.
func (r *Runtime) Session(ctx context.Context, opts ...agentkit.Option) *agentkit.Session {
	return agentkit.NewSession(r.prepare(ctx), r.llm, r.sessionOpts(opts)...)
}

// Once runs a one-shot to a final answer with the same wiring.
func (r *Runtime) Once(ctx context.Context, input string, opts ...agentkit.Option) (string, error) {
	return agentkit.Once(r.prepare(ctx), r.llm, input, r.sessionOpts(opts)...)
}

// prepare installs the gate machinery into ctx (no-op when gating is not configured).
func (r *Runtime) prepare(ctx context.Context) context.Context {
	if r.policy == nil {
		return ctx
	}
	ctx = gate.With(ctx, r.policy, r.grants, r.approver)
	return gate.WithLogger(ctx, r.gateLog)
}

func (r *Runtime) sessionOpts(extra []agentkit.Option) []agentkit.Option {
	opts := make([]agentkit.Option, 0, len(r.base)+len(extra)+2)
	opts = append(opts, r.base...)
	if r.toolsFn != nil {
		opts = append(opts, agentkit.WithToolsFunc(r.toolsFn))
	}
	if r.skillsFn != nil {
		opts = append(opts, agentkit.WithSkillsFunc(r.skillsFn))
	}
	return append(opts, extra...)
}
