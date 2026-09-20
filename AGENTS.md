# Nocturn — agent instructions

> **This file is the truth. `CLAUDE.md`, `GEMINI.md` and `.github/copilot-instructions.md` are
> symlinks to it — edit this one.** Skills live in `.agents/skills/` (canonical) and are symlinked
> into each agent's directory: `npx skills list`, `npx skills update`, `npx skills add <repo>`.
> Go style comes from the `cc-skills-golang` plugin, not from this tree.

A secure personal AI assistant in Go — a single binary, no foreign runtime, orchestrating an LLM
through permission-gated, human-approved tools. Its angle is *mandatory out-of-band approval on a
second device, WASM-isolated, without DB or cloud*. Two halves: `agentkit/` (the engine, its own
zero-dependency module) and `internal/` (nocturn — the security boundary and composition).

## Read before you touch

Nothing here is loaded automatically. Open the file that matches the work.

| You are touching | Read first |
|---|---|
| anything, for the first time | `.agents/docs/architecture.md` |
| `agentkit/**` | `.agents/docs/architecture.md`, `agentkit/DOCS.md` |
| the gate, `internal/{sandbox,secret,auth,hitl,library,plugin,extension}/**` | `.agents/docs/permissions.md` |
| `internal/tui/**`, any `*.gsx` | `.agents/docs/pitfalls.md` |
| `mobile/**`, `internal/webui/**` | skills `angular-developer`, `ionic-angular` |
| `mobile/` native side — Capacitor config, plugins, APNs | skills `capacitor-angular`, `capacitor-app-development`, `capacitor-push-notifications` |
| `docs/**` | `docs/AGENTS.md`, `docs/CONVENTIONS.md` |
| any Go file, before committing | `.agents/docs/conventions.md` |
| build, generators, catalog, hooks | `.agents/docs/workflow.md` |
| "why is it like this?" | `ADRS.md` — the decisions, distilled, and nowhere else |
| "how does this package work?" | its doc comment: `go doc ./internal/<pkg>` |

Everything else: `go doc ./internal/<pkg>` for what a package does, `git log` for what happened.

## The repo

```
cmd/nocturn        the binary: process spine, workspace open, `serve` daemon
internal/…         nocturn: the security boundary, persistence, composition
agentkit/…         the engine + gate/runtime/openai/tools/gemini (separate modules)
mobile/            the companion app (Angular + Capacitor, iOS) — the second device
docs/              the docs site (Astro/Starlight), schema-validated
catalog/           the published catalog's source, one folder per installable thing
sdk/_template/     the starting point for a plugin
media/             Remotion explainers — outside go.work, never linked
.agents/           agent instructions (docs/) and skills (skills/)
```

## The loop

```bash
git config core.hooksPath .githooks   # once per clone — the two commit gates

# `./...` is scoped to the CURRENT module; go.work does not fold the others in. Loop them, as CI does:
for m in . agentkit agentkit/{gate,openai,tools,runtime,gemini}; do (cd $m && go build ./... && go vet ./... && go test -race ./...); done
gofmt -l cmd internal agentkit        # must print nothing
(cd docs && npm run build)            # validates the tool and gate-kind YAML against its schema
```

Two gates will stop a commit, on purpose, and they are git hooks so they hold for every agent and for
a human: staged `.go` files must have been through the Go review skills first, and a commit touching
only `internal/` or `cmd/` has to account for the documentation. Details in
`.agents/docs/workflow.md`.

## How to work here

- **One aspect at a time.** Clarify it, cast it in code, prove it stable, then the next. A branch that
  does three things is three branches.
- **No backward-compat ballast in greenfield.** Replace the old API and migrate every call site.
- **Explicit over implicit, fail closed.** A forgotten field must never mean "allow / permanent /
  wildcard".
- **Check claims against the code before writing them down.** If code and docs disagree, the code
  wins and the doc is wrong.
- **Docs state truth, not change.** Fix the wrong sentence in place; never append a paragraph
  narrating what changed.
- **The security model is not a detail.** The policy is *ask on net and file, allow otherwise* — not
  deny-by-default. Tightening it is a deliberate change, not a bugfix.
- **Don't style Go from memory.** `cc-skills-golang:golang-how-to` routes to the skills that know the
  rule.

## Open

1. **More tools** (calendar) — a small gated tool in `internal/tools` serves model, script and plugin
   at once. `exec` stays deliberately unbuilt: ADR-7's bucket C is the only escape hatch and never the
   default.
2. **agentkit extraction** into its own repository.
3. **Attenuation** for skills and plugins, beyond today's entry signing.
4. **Keychain backend** for `secret`, instead of process memory.
5. **Hardening**: append-only audit sink, metrics.
6. Skill guest language (TinyGo vs Rust) · the skill PDK fork (Extism's 34-module TCB vs our own host
   + Javy) · escape hatch to a wasmtime-go/component backend.
