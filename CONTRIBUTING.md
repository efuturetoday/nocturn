# Contributing

## The loop

```bash
go build ./...                # go.work spans nocturn and the agentkit modules
go test -race ./...           # must be green before anything else is worth discussing
gofmt -l cmd internal agentkit # must print nothing
cd docs && npx astro build    # validates every tool and gate-kind YAML against its schema
```

The agentkit modules are siblings, not subpackages, so `./...` at the root does not reach them —
CI loops over `agentkit/{gate,openai,tools,runtime,gemini}` and you should too when you touch one.
`internal/onnx/reference/` is a nested module holding gomlx and needs `GOWORK=off`; it exists only
to regenerate a golden file, and is not part of the build.

Some tests skip themselves without a speaker-embedding checkpoint. That is deliberate — the file is
~26 MB and is never committed. `export NOCTURN_SPEAKER_MODEL=…` to run them.

## Two hooks will stop you, on purpose

`.claude/settings.json` is committed, and it is the process rather than a description of it.

- **Before a commit** with staged `.go` files: the commit is *denied* until that exact diff has been
  through the Go review skills — Effective Go and the Google Go Style Guide, cited by rule and
  `file:line`. Fix the findings or justify each one you leave; the same staged content is never
  blocked twice.
- **After a commit** touching only `internal/` or `cmd/`: blocked until you have said what
  documentation changes, or why none does.

If you work without those tools, do the same two things by hand. The point is that neither step is
optional, not which program performs it.

## What a change should look like

**One aspect at a time.** Clarify it, cast it in code, prove it stable, then move on. A branch that
does three things is three branches.

**No backward-compat ballast.** This is greenfield: replace the old API and migrate every call site.
A wrapper kept "just in case" is a second thing to keep correct.

**Explain the why, not the what.** The diff already says what changed. A comment earns its place by
recording the alternative that was rejected, the measurement that settled it, or the failure mode
that is not obvious. `CLAUDE.md` §6 is the running list of pitfalls actually hit — add to it when
you find a new one, so it is paid for once.

**Fail closed.** A forgotten field must never silently mean *allow*, *permanent*, or *wildcard*.
`agent.Strict` and `gate.RecallNever` are the zero values for exactly this reason.

**Tests.** External `_test` package for the public API, internal only for unexported things. Fakes
over interfaces; `httptest` for HTTP; time-dependent behaviour through `testing/synctest` rather
than `sleep`. Subtests must be independently runnable — no shared mutable fixture between siblings.

## Commits

Conventional Commits: `feat(speaker): …`, `fix(serve): …`, `docs(remote-access): …`. The subject
says what changed; the body says why, and what was considered instead. The earliest ~40 commits in
this history predate the convention and are left alone deliberately — rewriting them would rewrite
dates and trailers that are part of the record.

## Before reversing a decision

Read [`ADRS.md`](ADRS.md) first. If a choice is load-bearing, it is in there with its reasoning, and
the reasoning is what to argue with. Making a new load-bearing choice means adding one.

Anything touching the sandbox, the gate, the vault, approvals, or the workspace mount is
security-shaped: say in the pull request which boundary it moves and why that is safe. Reports of a
*vulnerability* belong in [SECURITY.md](SECURITY.md), not in a public issue.
