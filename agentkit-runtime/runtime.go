// Package runtime wires the pieces — an agentkit LLM, a toolset, skills, permission gating and
// session defaults — into ready-to-run sessions. Build a Runtime once, then start Sessions or
// one-shot Once calls that all share the same tools, policy, grants and approver. It is the
// composition root: the plumbing lives here so a consumer configures, not wires.
package runtime

import (
	"context"

	"github.com/efuturetoday/agentkit"
	"github.com/efuturetoday/agentkit-gate"
)

// Runtime holds the shared configuration for a set of sessions.
type Runtime struct {
	llm      agentkit.LLM
	tools    agentkit.ToolSet
	skills   agentkit.SkillSet
	policy   gate.Policy
	grants   gate.Grants
	approver gate.Approver
	base     []agentkit.Option // default session options
}

// Option configures a Runtime.
type Option func(*Runtime)

// WithTools sets the toolset. When gating is configured (WithGate), the tools are wrapped so each
// call is gated on its name; a tool that self-checks a runtime target (a host) is unaffected —
// classify its target axis, not its name, so the wrapper's name check just passes.
func WithTools(ts agentkit.ToolSet) Option { return func(r *Runtime) { r.tools = ts } }

// WithSkills sets the skills.
func WithSkills(ss agentkit.SkillSet) Option { return func(r *Runtime) { r.skills = ss } }

// WithGate installs permission gating: a Policy plus a Grants store (nil = in-memory) and an Approver
// (nil = unattended, so any Ask is denied). Without it, tools run ungated.
func WithGate(policy gate.Policy, grants gate.Grants, approver gate.Approver) Option {
	return func(r *Runtime) { r.policy, r.grants, r.approver = policy, grants, approver }
}

// WithSession adds default agentkit session options (system prompt, timeout, token limit, tokenizer,
// store, …) applied to every Session/Once.
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
		if r.tools != nil {
			r.tools = gate.WrapAll(r.tools)
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
	return gate.With(ctx, r.policy, r.grants, r.approver)
}

func (r *Runtime) sessionOpts(extra []agentkit.Option) []agentkit.Option {
	opts := make([]agentkit.Option, 0, len(r.base)+len(extra)+2)
	opts = append(opts, r.base...)
	if r.tools != nil {
		opts = append(opts, agentkit.WithTools(r.tools))
	}
	if r.skills != nil {
		opts = append(opts, agentkit.WithSkills(r.skills))
	}
	return append(opts, extra...)
}
