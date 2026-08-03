# Nocturn — Project Knowledge (CLAUDE.md)

> Only what you cannot derive from the code. Everything here is checked against the tree.
>
> **Single sources of truth — do NOT duplicate here:**
> - What a package does → `go doc ./internal/<pkg>` · `go doc ./agentkit[/<mod>]`
> - Why X over Y → **`ADRS.md`** · agentkit's design → **`agentkit/DOCS.md`**
> - What already happened → **git log**

---

## 1. What Nocturn is

A secure personal AI assistant in Go — a single binary, no foreign runtime, orchestrating an LLM
through permission-gated, human-approved tools.

**The defensible angle:** *mandatory out-of-band approval on a second device, WASM-isolated, in a
single Go binary without DB or cloud.* Others sandbox-and-automate, or ask only in-app, or offer
out-of-band as an optional extra.

**Two threat classes → two defenses (the core insight):**
- Malicious plugin/skill *code* → the **WASM sandbox** (isolates the code).
- Prompt injection abusing *legitimate* tools → the **gate + out-of-band approval** (isolates the
  effect). In-band approval sits in the same trust domain as the injection — hence a second device.

---

## 2. The two halves

**`agentkit/` — the engine. Its own module, `go.mod` with no `require` block at all: zero
dependencies.** An LLM-agnostic turn loop, immutable tool/skill sets, sub-agents (a sub-agent is
just a tool), a one-way event stream, per-turn and per-tree guards. Everything external is a port
(`LLM`, `Tool`, `Logger`, `Store`). The core is **policy-blind** — it knows nothing about
permissions. Sibling modules: `gate` (needs agentkit), `runtime` (agentkit+gate), `tools`
(agentkit+gate), `openai` (agentkit + go-openai — the only place go-openai appears). Destined for
its own repository, which is why nothing nocturn-specific may leak into it.

**`internal/` — nocturn.** The security boundary (sandbox, secrets, gated tools), the things
agentkit deliberately leaves to its consumer (transcript persistence, skill sources, discovery),
and composition per workspace.

```
cmd/nocturn        the binary: process spine, workspace open, terminal chat, `serve` daemon
internal/…         see §3
agentkit/…         the engine + gate/runtime/openai/tools
mobile/            the companion app (Angular + Capacitor, iOS) — the second device
docs/              the docs site (Astro/Starlight); tool/capability data is schema-validated
sdk/_template/     the starting point for a plugin (manifest + JS + TS source)
```

### Request flow

```
   user turn (terminal REPL, or the mobile app over WebSocket)
        │
   cmd/nocturn ──> workspace.Open assembles the per-workspace stack
        │           (tools = the cage · gate = policy+grants+approver · persona · chat store)
   internal/chat ──> Manager starts/resumes an agentkit Session over the file-backed Store
        │
   agentkit.Session ──> turn loop: ask model → tool call | answer → tool → back
        │                tokens/reasoning stream on the ctx event sink
        ├─ agentkit/openai ── streaming SSE, native tool_calls
        │
   gate.Check(Action{Kind, Target}) ──> Policy → allow | ask | deny
        │    an "ask" consults remembered Grants first, then a human
        ├─ internal/hitl ── first answer wins across attached app connections,
        │                   or internal/push (APNs) wakes a paired device
        │
   secret.Injector ── credential injected host-side at the boundary (the guest never sees it)
        ▼
   the effect (HTTP/DNS/file/…) → egress scan / ingress redact → result back into the loop
```

---

## 3. Package index

> One line each. What a package really does is its doc comment — `go doc` it.

**agentkit (separate module):** `agentkit` (loop, ports, sets, sub-agents, events, guards,
pausable budget) · `gate` (Policy→Ruling, Grants with recall, Approver, `Check`/`Wrap`) ·
`runtime` (wires LLM+tools+skills+gate into ready-to-run sessions) · `openai` (adapter) ·
`tools` (generic gated tools: `HTTPGet`, host matching).

**Security boundary:** `sandbox` (wazero guest at zero authority + WASI + brokered imports +
memory cap + wall-clock deadline) · `secret` (+`/oauth`) (store — a guest learns a secret exists,
never its value — encrypted vault, host-owned injector, bidirectional leak scanner) ·
`auth` (device registry; bearers stored only as sha256 hashes, constant-time compared) ·
`hitl` (out-of-band approval broker implementing `gate.Approver`) ·
`push` (APNs; a push is a WAKE, never a decision).

**Tools & extensions:** `tools` (the thin gated tools) · `script` (untrusted JS on QuickJS/wasm,
exactly ONE host import: `nocturn.call`) · `plugin` (sandboxed plugins; the manifest declares
tools/`uses` cage/credentials and is reviewed WITHOUT running the artifact) ·
`mcp` (+`/authflow`) (stdlib-only protocol client over an injected transport + the gated
connection layer).

**Context & composition:** `memory` (the assistant's durable notes; catalog DERIVED from the notes
on disk and folded into every prompt, bodies on demand — control-plane folder, one writer) ·
`frontmatter` (the shared `---` YAML preamble parser/renderer: skills and memory notes) ·
`skill` (agentskills.io skills from disk → `agentkit.SkillSet`) ·
`discovery` (the shared name/skip rules for agents, skills, plugins, MCP — one rule, four kinds) ·
`knowledge` (+`/embed`) (retrieval over `mnt/knowledge`: Markdown-aware chunking behind a `Reader`
port, an `Embedder` port with a remote OpenAI-compatible adapter, hybrid cosine+BM25 fused by
reciprocal rank, an index OUTSIDE the mount that records its model and refuses to mix embedders, and
a one-minute reconcile that costs a directory walk when nothing changed) ·
`chat` (file-backed transcript store + Manager) · `agent` (declaration + cron only; execution is
injected by the workspace) · `workspace` (the composition root) · `serve` (WebSocket surface,
tagged JSON, one file per domain) ·
`speaker` (who spoke: Kaldi-compatible filterbank → embedding → cosine, plus `voices.json` per
workspace. 100% top-1 among 2–6 enrolled voices. **The threshold belongs to a CHANNEL, not to the
package** — 0.50 fits close-talking, the satellite needs 0.45, measured. Chooses context and address,
never permission: speech is a channel like the chat, where nobody authenticates the typist either.
Measurements: `internal/speaker/testdata/README.md`) ·
`onnx` (the inference engine `speaker` runs on: a deliberately narrow ONNX subset in pure Go, no
CGO — the package doc says why not onnxruntime, wasm or a tensor framework, with the measurements).

**The tools that exist today** (`internal/tools`, plus the two heavy ones):

| Tool | Gate |
|---|---|
| `http_read` `http_write` `dns_resolve` `ping` | `NetKind`, target = host |
| `file_read` `file_list` `file_stat` `file_search` `file_write` `file_remove` `file_move` | `FileKind`, target = path, workspace-confined |
| `notify` | `NotifyKind` |
| `remind` `remind_list` `remind_cancel` | `RemindKind` |
| `memory_write` (`internal/memory`) | `memory.Kind`, target = note path, **outside `mnt`**; allowed in chat, asked in agent runs |
| `memory_read` (`internal/memory`) | **ungated** — context, never authority (same argument as `skill_read`) |
| `time_now` `wake` | **ungated** — zero authority (no wall-clock in the guest), `wake` bounded |
| `whoami` (`internal/speaker`) | **ungated** — only registered when `NOCTURN_SPEAKER_MODEL` is set |
| `knowledge_search` (`internal/knowledge`) | **ungated** — context, never authority; only registered when an embedding endpoint is configured |
| `code_run` (`internal/script`) | woven per cage by `tools.Compose`, so a script's reach is its cage |
| `skill_read` (`internal/skill`), `skill_load` (agentkit) | context, never authority |

**Dependencies:** agentkit core: **none**. nocturn: `wazero`, `coder/websocket`,
`libp2p/zeroconf/v2`, `x/crypto`, `x/net`, `x/oauth2`, `aho-corasick` (leak scanner only),
`yaml.v3` (skill frontmatter only), `lmittmann/tint`, `godotenv`. `go-openai` is indirect.
**Rejected:** langchaingo (290 deps, brings its own loop that bypasses our security).
**Dev tools:** `wat2wasm` (brew wabt) for the WAT test guests; `wasi-sdk` + a quickjs-ng checkout
to rebuild the interpreter wasm (only when the shim changes — the built `.wasm` is committed).

---

## 4. The permission model as it actually is

Read this before touching anything security-shaped — it is easy to assume the stricter version.

- **Two separate questions.** WHICH tools an agent has at all = agentkit's `ToolSet` (bound once,
  statically, via `Select`). WHAT a tool may DO = `gate` (per action, asked when risky, remembered).
- **The workspace root policy** (`internal/workspace/workspace.go:policy`): `NetKind` and `FileKind`
  → **ask, remembered for the session**; every other kind → **allow**. It is *not* deny-by-default.
  Tightening it is a deliberate change, not a bugfix.
- **`agentPolicy` is `policy` plus `memory.Kind` → ask** — the one axis staggered by who is watching.
  A chat shows the write in its transcript as it happens, so asking would only buy "before" instead
  of "after"; an unattended run has no reader, so it asks out of band (and with no device, denies).
- **The system prompt is live, the identity is not.** `resolvePersona` is evaluated ONCE at `Open`;
  the memory index is folded in per turn via `agentkit.WithSystemFunc` (`composePrompt`). The block
  is omitted when memory is empty or the runner's cage holds no memory tool — a narrowed agent must
  not be handed the user's notes. Agent runs and sub-agents get the same treatment, so a cron agent
  and the evening chat share one picture of the user.
- **Grants** are durable per workspace (`grants.json`, written 0600 via a temp file) and
  implement `gate.Grants`. Recall: never / session / always.
- **Agent autonomy** (`internal/agent`): `Strict` (the zero value) gets **no approver**, so a
  fresh ask is denied fail-closed; `Guarded` routes the ask out-of-band to the human. A missing or
  typo'd dial therefore never escalates authority. With no device wired, guarded collapses to
  strict.
- **Zero ambient authority:** the wazero guest gets nothing; every capability is an explicitly
  handed host window — unforgeable by absence.
- **Secrets** never enter the guest: presence only, value injected host-side at the boundary.
  Egress carrying a secret is blocked, ingress is redacted.

---

## 5. Patterns held throughout

- **Explicit constants over overloaded zero values, fail closed.** A forgotten field must never
  silently mean "allow / permanent / wildcard". `agent.Strict` and `gate.RecallNever` are the zero
  values on purpose.
- **No backward-compat cruft in greenfield.** Replace the old API and migrate every call site;
  leave no redundant wrappers standing.
- **Ports & adapters.** `agentkit.LLM`/`gate.Approver` are ports; the terminal approver and `hitl`
  are two adapters the runtime cannot tell apart. Compile-time asserts pin it
  (`var _ gate.Approver = (*Broker)(nil)`).
- **Policy-blind core.** Gating is a wrapper on agentkit, never inside it — that separation is what
  lets agentkit leave for its own repo.
- **Immutable sets.** `ToolSet`/`SkillSet`/`agent.Set` are built once, never mutated — nothing can
  inject into them at runtime.
- **Small things over god objects.** Each gated tool owns its own kind constant and target matcher;
  they share the gate model, not a growing struct.
- **Functional options** for configuration (`openai.WithEffort`, `runtime.WithGate`).
- **Onion building:** clarify one aspect → cast it in code → prove it stable → then the next layer.
  "Don't touch the lower shell" means *keep it stable*, not *keep dead code*.

---

## 6. Pitfalls actually hit

- **wazero's `Memory.Read` returns a view, not a copy.** Copy the bytes out immediately before the
  host function returns; never hold them past the call (`memory.grow` reallocates). Within one host
  call the guest is suspended → race-free. One instance = one goroutine.
- **`go mod tidy` removes deps nobody imports yet** — add the import first, then `go get` + `tidy`.
- **Anchor `.gitignore` rules at the root** (`/plugins/`, `/workspaces/`), otherwise `plugins/`
  matches every directory of that name at any depth — `internal/workspace/` was accidentally
  ignored once and never committed (HEAD did not build from a fresh clone).
- **`git mv a/x b/x` nests when `b/x` already exists** — a leftover directory holding only a
  `.DS_Store` is enough, and you silently get `b/x/x`. Check for leftovers before a bulk move.
- **gopls diagnostics can be stale — the compiler is the truth.** "undefined: X" with a green
  `go build ./...` = stale index. Never restructure on suspicion; build first.
- **LLMs are never fireproof.** Robustness = structured `tool_calls` + schema as a guardrail + our
  own argument validation and retry (the error is fed back to the model).
- **`.env` is gitignored**; `godotenv.Load()` reads from CWD and real env vars win.
- **A model's documented default is not the default it was trained with.** `internal/speaker`
  implemented Kaldi's filterbank from the specification and used Kaldi's own default window,
  `povey`; WeSpeaker overrides it to `hamming`. All nine property tests passed — window shape, tone
  in the right Mel bin, gain absorbed — and same-speaker similarity sat at 0.73 instead of 0.98.
  Read the training code, and pin the frontend against the implementation, not against a reading
  of it. Anything that feeds a pretrained network deserves this suspicion.
- **A nested `go.mod` needs `GOWORK=off`.** `internal/onnx/reference/` is its own module so gomlx
  stays out of nocturn's graph. That works — the parent skips it — but `go.work` does not cover it
  either, so plain `go build ./...` inside it fails with "directory prefix . does not contain
  modules listed in go.work".

---

## 7. Testing conventions

- **External test package** (`_test`) for the public API; internal (same package name) only for
  unexported things.
- **Fakes over interfaces**: mock `LLM`/`Approver`/`Sender`; HTTP via `httptest`.
- **Blocking things** (approval, streaming) coordinated with goroutines + channels, never `sleep`;
  `-race` runs green.
- **Time-dependent tests via `testing/synctest`** (Go 1.25+, real `time`, fake clock in the
  bubble) — no clock injection. Used in `tools`, `chat`, `auth`, `hitl`, `agent`, `push`,
  `sandbox`. Production clock injection (`agent.Scheduler`) is separate and legitimate.
- Compile-time asserts on every port implementation.

---

## 8. Dev workflow

> **Use the Go skills.** For Go work use the installed `cc-skills-golang` skills — above all
> **`go-review`** before committing non-trivial Go (checks Effective Go + the Google Style Guide
> and cites the rule), plus the topical ones when the task touches them. `golang-how-to` loads the
> right set. Don't style from memory when a skill knows the rule.

```bash
go build ./...           # go.work spans nocturn + the agentkit modules
go test ./...            # race-clean
go test -race ./...

wat2wasm internal/sandbox/testdata/echo.wat -o internal/sandbox/testdata/echo.wasm
internal/script/qjs/build.sh          # only when the QuickJS shim changes (needs wasi-sdk)

# Speaker recognition. The checkpoint (~26 MB) and the evaluation corpus are NOT committed; the
# tests needing them skip when unset. Everything about regenerating the two golden files, and the
# measured thresholds, is in internal/{onnx,speaker}/testdata/README.md.
export NOCTURN_SPEAKER_MODEL=…/wespeaker_en_voxceleb_resnet34.onnx
go test ./internal/speaker/ ./internal/onnx/
internal/speaker/reference/corpus.sh /tmp/corpus   # 40 LibriSpeech speakers, needs ffmpeg
NOCTURN_SPEAKER_CORPUS=/tmp/corpus go test ./internal/speaker/ -run Evaluate -v -timeout 30m
internal/speaker/reference/setup.sh   # torch venv, ONLY to regenerate the filterbank reference

cp .env.example .env                  # OPENAI_BASE_URL / _MODEL / _API_KEY
go run ./cmd/nocturn                  # interactive terminal chat
go run ./cmd/nocturn serve            # the WebSocket daemon the mobile app talks to
#   in chat: /chats /new /open <id> /agents /fire <name> /quit
#   subcommands: auth <provider> · secret set|ls · ls · version · help (most take -w)

cd docs && npx astro build            # the schema validates every tool/capability entry
```

---

## 9. Open

1. **More tools** (mail, calendar) — a small gated tool in `internal/tools` serves model, script
   and plugin at once. (`exec` stays **deliberately unbuilt** — ADR-7's bucket C is the only
   escape hatch and never the default.)
2. **agentkit extraction** into its own repository.
3. **Signing + attenuation** for skills and plugins (Ed25519), and distribution.
4. **Keychain backend** for `secret` (instead of process memory).
5. **Hardening**: SECURITY.md, append-only audit sink, metrics.
6. Skill guest language (TinyGo vs Rust) · the skill PDK fork (Extism's 34-module TCB vs our own
   host + Javy — DevEx against a small TCB) · escape hatch to a wasmtime-go/component backend.

**Way of working:** one aspect at a time — clarify, build, prove stable. Explicit over implicit.
No sprawl, no cruft, no backward-compat ballast in greenfield.
