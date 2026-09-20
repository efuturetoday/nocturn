# Conventions

## Patterns held throughout

- **Explicit constants over overloaded zero values, fail closed.** A forgotten field must never
  silently mean "allow / permanent / wildcard". `agent.Strict` and `gate.RecallNever` are the zero
  values on purpose.
- **No backward-compat cruft in greenfield.** Replace the old API and migrate every call site; leave
  no redundant wrappers standing.
- **Ports & adapters.** `agentkit.LLM` / `gate.Approver` are ports; the terminal approver and `hitl`
  are two adapters the runtime cannot tell apart. Compile-time asserts pin it
  (`var _ gate.Approver = (*Broker)(nil)`).
- **Policy-blind core.** Gating is a wrapper on agentkit, never inside it (ADR-11).
- **Immutable sets.** `ToolSet` / `SkillSet` / `agent.Set` are built once, never mutated.
- **Small things over god objects.** Each gated tool owns its own kind constant and target matcher;
  they share the gate model, not a growing struct.
- **Functional options** for configuration (`openai.WithEffort`, `runtime.WithGate`).
- **Onion building:** clarify one aspect → cast it in code → prove it stable → then the next layer.
  "Don't touch the lower shell" means *keep it stable*, not *keep dead code*.

## Go style

Use the installed Go skills rather than styling from memory. `cc-skills-golang:golang-how-to` is the
orchestrator — it knows which skills exist and which match a diff. A commit is always a style review,
so its review row applies on top of whatever the subject matter adds:
`cc-skills-golang:golang-code-style`, `cc-skills-golang:golang-naming`, `cc-skills-golang:golang-lint`.

This is not advice: the pre-commit gate denies a commit with unreviewed staged `.go` files
(`.agents/docs/workflow.md`).

## Testing

- **External test package** (`_test`) for the public API; internal (same package name) only for
  unexported things.
- **Fakes over interfaces**: mock `LLM` / `Approver` / `Sender`; HTTP via `httptest`.
- **Blocking things** (approval, streaming) coordinated with goroutines + channels, never `sleep`;
  `-race` runs green.
- **Time-dependent tests via `testing/synctest`** (Go 1.25+, real `time`, fake clock in the bubble) —
  no clock injection. Used in `tools`, `chat`, `auth`, `hitl`, `agent`, `push`, `sandbox`. Production
  clock injection (`agent.Scheduler`) is separate and legitimate.
- Compile-time asserts on every port implementation.

## Documentation

- **Docs state truth, not change.** Fix the wrong sentence in place; never append a paragraph
  narrating what changed.
- If code and docs disagree, the code wins and the doc is wrong (`docs/AGENTS.md`).
- The *why* goes to `ADRS.md` and only there. `.agents/docs/**` is fact plus file pointer.
