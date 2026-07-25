# Nocturn — Project Knowledge (CLAUDE.md)

> Distilled, **non-derivable** knowledge: vision, threat model, patterns, pitfalls.
> Read this before building further.
>
> **Single sources of truth — do NOT duplicate here:**
> - What a package does → **`go doc ./internal/<pkg>`** and **`go doc ./agentkit`** (the package
>   doc comments are the distilled per-package truth; §4 is an index, not a transcription).
> - Why we chose X over Y → **`ADRS.md`** (the decision records).
> - What already happened → **git log** (no "done" history here).
>
> agentkit's own design: **`agentkit/DOCS.md`**.

---

## 1. What Nocturn is

A **secure personal AI assistant** in **Go** — the counter-design to OpenClaw ("sloppified AI
crap": insecure, complex, heavy UI). A single binary, no foreign runtime, orchestrating an LLM
through **permission-gated, human-in-the-loop** tools.

### Vision — 4 pillars (north star)
1. **Simplicity / easy deployment, no runtime** — one Go binary, no Node/Postgres/Docker/CGo.
2. **Sustainable ecosystem, great DevEx, polyglot** (JS/TS/Rust/Go).
3. **A small companion app** — shipped: the mobile app is where approvals happen.
4. **Small, focused, secure, comprehensible** — minimal TCB, not everything built out, but
   controllable.

---

## 2. Positioning — what we do differently

| | OpenClaw | IronClaw (nearai, Rust) | **Nocturn** |
|---|---|---|---|
| Stack | TS/Node | Rust + wasmtime + **Postgres** + Node web UI | **Go, single binary, wazero** |
| Isolation | none (exec/bash) | WASM component + vault + TEE | WASM (wazero) zero authority |
| Effect gating | weak allowlists | static per-tool allowlist | **dynamic target gating + HITL** |
| Human-in-the-loop | iOS/Watch, **exec-only, disableable**, in-band | in-band `ask`, **auto-approve on by default** | **mandatory, out-of-band, not disableable** |
| Polyglot skills | Markdown (no code) | **Rust-only, no PDK** | JS plugins today, polyglot planned |
| TCB | large | 54 crates | **minimal** (agentkit core has *zero* deps) |

**The defensible angle:** *mandatory, non-disableable out-of-band approval on a second device, at
EVERY trust boundary, WASM-isolated, as a single Go binary without DB or cloud.* Neither OpenClaw
nor IronClaw has that (both: "human out / optional / in-band").

**Two threat classes → two defenses (the core insight):**
- Malicious skill/plugin *code* / supply chain → **WASM sandbox** (isolates the code).
- Prompt injection abusing *legitimate* tools → **gate + out-of-band HITL** (isolates the *effect*).
  In-band approval sits in the same trust domain as the injection → hence a **separate device**.

---

## 3. Architecture — the two halves

Since the greenfield rebuild there are two clearly separated halves:

**`agentkit/` — the reusable engine (its own module, zero dependencies).**
An LLM-agnostic turn loop, immutable tool and skill sets, sub-agents (a sub-agent is just a
tool), a one-way event stream, per-turn and per-tree guards (steps, wall clock, tokens, spawn
depth/population). Everything external is a port: `LLM`, `Tool`, `Logger`, `Store`. The core is
**policy-blind** — it knows nothing about permissions. Adapters live in sibling modules
(`gate`, `runtime`, `openai`, `tools`). Destined for its own repository.

**`internal/` — nocturn: the product built on it.**
The security boundary (sandbox, secrets, gated tools), the things agentkit deliberately leaves
to the consumer (transcript persistence, skill sources, discovery), and composition per
workspace.

```
cmd/nocturn      the binary: loads .env, builds the process spine (master key, LLM client),
                 opens workspaces, runs a line-based terminal chat + `serve` for the app

internal/workspace   composition root PER workspace: owns tools (the cage), the gate
                     (policy + durable grants + approver), persona, chat store and manager
internal/chat        transcript persistence + multiplexing over agentkit.Store
internal/serve       one backend, many fronts: workspace chats over WebSocket (daemon-as-truth)
internal/auth        device registry: every WebSocket connection gated on a paired bearer
internal/hitl        routes a gate approval to a human out of band (attached app, or push wake)
internal/push        APNs adapter — a push is only a WAKE, never the decision itself
internal/tools       the thin gated tools (net/file/notify/remind/time/wake)
internal/plugin      installed sandboxed plugins (plugin.js/.wasm + plugin.json manifest)
internal/mcp         remote-MCP client (protocol) + its gated connection layer
internal/script      untrusted JS on the sandbox; ONE host import: nocturn.call
internal/sandbox     wazero guest under zero authority + WASI + hardening
internal/secret      store (presence only) + encrypted vault + injector + leak scanner
internal/skill       agentskills.io skills from disk → agentkit.SkillSet (context, never authority)
internal/agent       declaration + cron schedule only; execution lives in workspace/chat

agentkit             the loop, ports, sets, guards, events        (0 deps)
agentkit/gate        policy → allow/ask/deny, grants, approver     (the permission layer)
agentkit/runtime     wires LLM + tools + skills + gate into ready-to-run sessions
agentkit/openai      OpenAI-compatible adapter (the sole go-openai dependency in the tree)
agentkit/tools       ready-made tools that gate their own target (HTTPGet, host matching)
```

### Request flow

```
   User "fetch me X"        (terminal REPL, or the mobile app over WebSocket)
        │
        ▼
   cmd/nocturn ── loads .env; builds the process spine; workspace.Open assembles
        │          the per-workspace stack (tools, gate, persona, chat store)
        ▼
   internal/chat ── Manager starts or resumes an agentkit Session over the file-backed Store
        │
        ▼
   agentkit.Session ── the turn loop: ask model → tool call | answer → tool → back
        │              LLM is a port; answer/reasoning tokens stream on the ctx event sink
        ├──▶ agentkit/openai ── streaming SSE, native tool_calls, reasoning deltas
        ▼
   agentkit/gate.Check ── the one decision point, per ACTION (not per tool):
        │   Policy.Evaluate → allow · deny · ask
        │   "ask" first consults remembered Grants (session or always), else prompts
        ├──▶ internal/hitl ── first-committed-wins across attached app connections,
        │                     or internal/push wakes a paired device (APNs)
        ▼
   internal/secret.Injector ── bearer injected host-side at the boundary (the guest never sees it)
        ▼
   the real effect (HTTP/DNS/file/…) → egress/ingress leak scan → result back into the loop
```

> Detail per package: `go doc ./internal/<pkg>` · `go doc ./agentkit/<mod>`.

---

## 4. Package index

> **Index only — what a package really DOES lives in its doc comment.** One line each, grouped
> by role. Nothing duplicated here (it would drift).

**agentkit (separate module, own repo eventually):**
- `agentkit` — the loop, `Session`/`Once`, ports (`LLM`, `Tool`, `Logger`, `Store`), immutable
  `ToolSet`/`SkillSet`, sub-agents, events, guards, pausable budget. Zero deps.
- `agentkit/gate` — human-approved, remembered permission: `Policy` → `Ruling`
  (allow/ask/deny), `Grants` (recall: never/session/always), `Approver`, `Check`/`Wrap`.
- `agentkit/runtime` — the wiring: one `Runtime`, many sessions sharing tools/policy/grants.
- `agentkit/openai` — OpenAI-compatible adapter → `agentkit.LLM`.
- `agentkit/tools` — generic gated tools (`HTTPGet`, `HostMatch`, host grant suggestions).

**Security boundary (nocturn):**
- `sandbox` — wazero guest at zero authority + WASI + brokered host imports + hardening
  (memory cap, wall-clock deadline). Performs no action itself.
- `secret` — kind-agnostic store (a guest learns a secret *exists*, never its value) +
  encrypted `Vault` + `Injector` (host-owned credential injection) + bidirectional leak `Scanner`.
- `secret/oauth` — host-managed OAuth2 (loopback PKCE + refresh); tokens are just secrets.
- `auth` — device registry; bearers stored only as hashes, first device paired via one-time code.
- `hitl` — out-of-band approval broker implementing `gate.Approver`.
- `push` — APNs adapter; carries no approval authority (wake only).

**Tools · interpreters · extensions:**
- `tools` — the thin gated tools sharing one gate model: `http_read`/`http_write`/`dns_resolve`/
  `ping`, `file.*` (workspace-confined), `notify`, `remind*`, `time_now`, `wake`. `Base` builds
  the set every chat and agent draws from.
- `script` — untrusted JS on QuickJS/wasm; exactly ONE host import (`nocturn.call`) dispatching
  onto the SAME gated toolset the model uses.
- `plugin` — installed sandboxed plugins; the manifest declares tools, cage (`uses`) and
  credentials, and is reviewed WITHOUT running the artifact.
- `mcp` (+ `mcp/authflow`) — remote-HTTP MCP: stdlib-only protocol client over an injected
  transport, plus the gated connection layer (every JSON-RPC POST is a gated network action).

**Context · composition · lifecycle:**
- `skill` — agentskills.io skills from disk → `agentkit.SkillSet` + `skill_read` (tier 3).
- `discovery` — the shared vocabulary of everything discoverable (agents, skills, plugins, MCP):
  how a skipped item is recorded, how a name is resolved. One rule, four kinds.
- `chat` — file-backed transcript store with chat metadata + `Manager` (start/resume sessions).
- `agent` — declaration only: `Agent`, discovery of `agent.md` into an immutable `Set`, cron
  `Scheduler`. Execution is injected by the workspace.
- `workspace` — the composition root per workspace: tools + gate + persona + chat store/manager
  over a shared `Host`; also OAuth/MCP account plumbing.
- `serve` — the WebSocket surface: tagged JSON, one file per domain (chat, approval, agent, …).

**`cmd/nocturn`** — the binary: process spine, workspace open, a line-based terminal chat with
slash commands (`/chats`, `/new`, `/open`, `/agents`, `/fire`, `/quit`), and the `serve` mode the
mobile app talks to.

**`mobile/`** — the companion app (Angular + Capacitor, iOS): pairing, chats, and the approval
UI. This is the second device that makes out-of-band HITL real.

**Dependencies (deliberately minimal):** agentkit core: **none**. nocturn: `wazero` (pure Go, no
CGo), `coder/websocket`, `libp2p/zeroconf` (mDNS), `golang.org/x/crypto` (scrypt),
`golang.org/x/net` (icmp for `ping`), `golang.org/x/oauth2`,
`petar-dambovaliev/aho-corasick` (leak scanner only), `gopkg.in/yaml.v3` (skill frontmatter
only — no RCE deserialization risk in Go, input size-capped, typed decode), `lmittmann/tint`
(log formatting), `godotenv`. `go-openai` is indirect — only `agentkit/openai` speaks to it.
**Rejected:** langchaingo (290 deps, brings its own agent loop that bypasses our security).
**Dev tools:** `wat2wasm` (brew wabt) for the WAT test guests; **`wasi-sdk` + a quickjs-ng
checkout** to rebuild the interpreter `.wasm` (`internal/script/qjs/build.sh`, only when the shim
changes — the built `.wasm` is committed). No runtime dependency: the binary stays pure Go/wazero.

---

## 5. Key design decisions (ADRs)

**Moved out → `ADRS.md`.** That file holds the decision *records* (the *why*: X over Y) — ADR-1…10
plus LLM provider, trust boundary (variant A), wazero-vs-Wasmtime and PORTICO. Add an ADR there
when you make or reverse a load-bearing decision. Short form of the resulting security model: §8.

---

## 6. Patterns & code style (held throughout)

- **Explicit constants over overloaded zero values, fail closed.** A forgotten field must never
  silently mean "allow everything / permanent / wildcard". `gate.Recall`'s zero value is
  `RecallNever`; a `Ruling`'s zero value is not "allow".
- **No backward-compat cruft in greenfield.** Replace the old API and migrate every (own) call
  site; leave no redundant wrappers standing (that is tech debt).
- **Ports & adapters.** `agentkit.LLM` is a port, `agentkit/openai` the adapter → the loop is
  provider-agnostic and mock-testable. `gate.Approver` is a port; the terminal and `hitl` are
  two adapters that the runtime cannot tell apart.
- **Policy-blind core.** agentkit knows nothing about permissions; gating is a wrapper on top.
  That separation is what lets agentkit leave for its own repo.
- **Two separate questions, deliberately.** WHICH tools an agent has at all = agentkit's
  `ToolSet` (bound once, statically). WHAT a tool may DO = `gate` (checked per action, asked when
  risky, remembered).
- **Immutable sets.** `ToolSet`/`SkillSet`/`agent.Set` are built once and never mutated —
  no registry that anything can inject into at runtime.
- **No god object.** Each gated tool is a small thing owning its own kind constant and target
  matcher; they share the gate model, not a growing struct.
- **Role names for fields** (`Secrets`, `Grants`, `Policy`) over generic/type names.
  Unambiguous package names.
- **Functional options** for configuration (`openai.WithEffort`, `runtime.WithGate`).
- **Onion/wall building:** clarify one aspect → cast it in code → prove it stable → only then the
  next layer. "Don't touch the lower shell" means *keep it stable*, not *keep dead code*.

---

## 7. Pitfalls & tweaks (actually hit)

- **wazero's `Memory.Read` returns a view, not a copy.** Copy the bytes out immediately
  (`string(buf)`) before the host function returns; never hold them past the call
  (`memory.grow` reallocates). Within one host call the guest is suspended → race-free. One
  instance = one goroutine (instances are not concurrency-safe).
- **`go mod tidy` removes deps nobody imports (yet).** godotenv got pruned until `main.go` used
  it → then `go get` + `tidy`.
- **`.env` is gitignored** (key never in code); `godotenv.Load()` reads from CWD, real env vars
  win. Commit `.env.example`.
- **A push is a wake, never a decision.** APNs carries no authority; the approve/deny happens
  in-app over the authenticated WebSocket, so the signed decision never leaves the daemon.
- **Bearers are stored as hashes only.** A leaked `devices.json` cannot be replayed.
- **LLMs are never 100% fireproof.** Robustness = structured `tool_calls` (no text guessing) +
  schema as a guardrail + **our own argument validation + retry** (the error is fed back to the
  model).
- **freellm endpoint:** OpenAI-compatible, model `auto` (dodges individual model cooldowns);
  `FREELLM_BASE_URL`/`_API_KEY`/`_MODEL` in `.env`.
- **Anchor `.gitignore` rules at the root** (`/plugins/`, `/workspaces/`), otherwise `plugins/`
  matches **every** directory of that name at any depth — `internal/workspace/` was accidentally
  ignored once and **never committed** (HEAD did not build from a fresh clone).
- **`git mv a/x b/x` nests when `b/x` already exists** (a leftover directory, e.g. holding only a
  `.DS_Store`, is enough) — you get `b/x/x`. Check for leftovers before a bulk move.
- **gopls diagnostics can be stale — the compiler is the truth.** "undefined: X" with a green
  `go build ./...` = stale index (`go clean -cache` helps on the next reload). Never restructure
  code on suspicion; build first.

---

## 8. Security model (short form)

- **Zero ambient authority:** the wazero guest gets nothing; every capability is an explicitly
  handed host window → "unforgeable by absence".
- **Gate per ACTION, not per tool:** policy → allow/ask/deny; "ask" consults remembered grants
  first (session or always), then a human. Deny wins, unknown = deny.
- **HITL:** out-of-band on a second device (the app), or the terminal when attended.
  Timeout/deny = fail closed. Mandatory, not disableable for irreversible or external actions.
- **Vault:** secrets host-side; the guest sees only presence; bearer injected at the boundary.
- **Leak scan:** bidirectional — egress blocked when a secret would leave, ingress redacted.

---

## 9. Testing conventions

- **External test package** (`_test`) for the public API; **internal** (same package name) only
  for unexported things.
- **Table-driven** where there are many cases.
- **Fakes over interfaces**: mock `LLM`/`Approver`/`Sender`; HTTP via `httptest`.
- **Blocking functions** (approval, streaming) coordinated with goroutines + channels
  (no `sleep`); `-race` runs green.
- **Time-dependent tests via `testing/synctest`** (Go 1.25+, `synctest.Test` + real `time`, fake
  clock inside the bubble) — **no** clock injection (go.dev/blog/testing-time). Production clock
  injection (`agent.Scheduler`) is separate and legitimate.
- Compile-time asserts: `var _ agentkit.LLM = (*openai.Client)(nil)`.

---

## 10. Dev workflow

> **Use the Go skills whenever possible.** For Go work, use the installed `cc-skills-golang`
> skills — above all **`go-review`** before committing non-trivial Go (checks against Effective Go
> + the Google Style Guide, cites the rule), plus the topical ones (`golang-concurrency`,
> `golang-error-handling`, `golang-testing`, `golang-naming`, …) when the task touches them.
> `golang-how-to` is the orchestrator that loads the right ones. Don't style from memory when a
> skill knows the rule.

```bash
go build ./...            # build everything (go.work spans nocturn + agentkit modules)
go test ./...             # all tests (race-clean)
go test -race ./...

# rebuild the sandbox WAT test guests (after a change):
wat2wasm internal/sandbox/testdata/echo.wat -o internal/sandbox/testdata/echo.wasm

# rebuild the QuickJS interpreter wasm (only when the shim changes; needs wasi-sdk):
internal/script/qjs/build.sh

# the assistant:
cp .env.example .env   # fill in FREELLM_API_KEY
go run ./cmd/nocturn            # interactive terminal chat (default workspace)
go run ./cmd/nocturn serve      # the out-of-band WebSocket daemon the mobile app talks to
#   in-chat slash commands: /chats /new /open <id> /agents /fire <name> /quit
#   other subcommands: auth <provider> · secret set|ls · ls · version · help (most take -w)

# the docs site (Astro/Starlight):
cd docs && npx astro build      # the schema validates every tool/capability entry
```

---

## 11. Status & next steps

> What already happened is in **git log** — here only the rough current state and what is open.

**Standing (race-clean, tested):** `agentkit` as a zero-dependency engine (loop, ports, immutable
sets, sub-agents, events, guards) with `gate`/`runtime`/`openai`/`tools` modules · nocturn rebuilt
on it: sandbox, secrets + vault + leak scan, gated tools, `code_run` (QuickJS on the sandbox),
plugins, remote MCP, skills, OAuth, per-workspace composition, chat persistence + multiplexing ·
`serve` (daemon-as-truth over WebSocket) with device pairing, and the **mobile app** as the
out-of-band approval surface with APNs wake.

**Open / next:**
1. **More tools** (mail, calendar) — the pattern is a small gated tool in `internal/tools`, which
   serves model, script and plugin at once. (`exec` = **deliberately never**, ADR-7 bucket C
   stays empty.)
2. **agentkit extraction** into its own repository (it is already its own module with zero deps).
3. **Distribution** (IronHub-style + code signing) + **skill/plugin signing and attenuation**
   (Ed25519) — the M2 remainder.
4. **Keychain backend** for `secret` (instead of process memory).
5. **Hardening**: SECURITY.md, append-only audit sink, metrics.

**Way of working:** onion-shell — clarify one aspect → build → prove it stable. Explicit over
implicit. No sprawl, no cruft, no backward-compat ballast in greenfield.

---

# Appendix — Research & roadmap (durable knowledge)

> Consolidated here from the original plan. Reference material, not needed daily — but too
> valuable to rot in a plan file. `★` counts and timestamps are rough (fetch summarizers
> confabulate numbers — verify via the GitHub API before using them externally). Everything else
> is repo- or primary-source-verified.
>
> **Note:** the **competitive research (A/B/C, positioning)** is the lasting part. The
> **roadmap/milestones (E) and the proof checklist (F)** are a historical snapshot — for the real
> current state see **§11 + git log**, not the checkmarks here.

## A — Competition (deep dive)

### IronClaw (`nearai/ironclaw`) — the direct rival, the benchmark
Rust reimplementation of OpenClaw, 54-crate workspace, Apache-2.0/MIT, ~12.5k★, v0.29.1
(June 2026, **pre-1.0**). Illia Polosukhin (NEAR) associated. Hosted = NEAR AI Cloud (TEE).
- **Isolation:** Wasmtime 46, component model + WASI Preview 2. WASM tools see only **4 host
  functions** (`log`, `now_unix_secs`, `workspace_read/write`); network via a host-side HTTP proxy.
  Per-tool `capabilities.json`. Memory limit via `ResourceLimiter`, **CPU via fuel** (100M instr).
  Pipeline: `WASM→allowlist→leak scan→credential→execute→leak scan→WASM`.
- **Strengths (do not attack head-on):** (a) **credential vault** `ironclaw_secrets`: AES-256-GCM,
  per-secret HKDF-SHA256, domain-separated AAD, low-entropy guard, secret never in WASM memory —
  **the table-stakes model for us**. (b) **Bidirectional leak scanning** (15+ patterns, in and
  out). (c) Rust memory safety + mature component isolation + fuel. (d) Feature breadth (MCP
  client, many providers/channels, NL→WASM tool autogen, hybrid vector search).
- **Weaknesses (verified, beatable):** (a) **No out-of-band/phone HITL** (`phone|twilio|push|sms`
  = 0 hits). "Approval" = a persistent grant store `ironclaw_approvals` (allow/ask/deny) that
  **does not prompt the user**, **auto-approve on by default**, and "ask" is *in-band*
  (attackable). Background triggers → no human, the system only *denies* high severity.
  (b) `PolicyAction::Review` = **stub**. (c) **Skill attenuation not ported** (#5581), capability
  catalog leaks (#5712), **no code signing**. (d) **No SECURITY.md**, private vuln reporting off
  (#6000). (e) Multi-tenant leak (#5460), audit sink gaps (#5640/#5428). (f) **Heavyweight:**
  Postgres 15 + pgvector + 54 crates + Node/pnpm web UI; "simple" = NEAR cloud/TEE lock-in. A
  "reborn" rewrite is underway → the design is unsettled.

### OpenClaw itself — the HITL incumbent (surprisingly)
- iOS app + **Apple Watch** review and approve pending **`exec` requests** from the phone
  (`operator.approvals`, "first committed answer wins"). Real out-of-band approval —
  **already shipped**.
- **Weaknesses (our counter):** only `tools.exec` (not all boundaries) · **disableable**
  ("never stop on exec approvals") · TS monolith, **no WASM sandbox**. → We: *mandatory, not
  disableable, at ALL boundaries, WASM-isolated*.

### WASM sandbox tech (isolation competition)
- **MS Wassette** — an MCP server running WASI components as tools (Wasmtime). WIT→JSON schema
  automapping, OCI distribution **signed (Notation/Cosign)**, deny-by-default, YAML policy. The
  cleanest standards-conformant capability injection, but **"not production ready"**,
  Rust+Wasmtime+CGo, **no HITL**.
- **wasmCloud/Cosmonic** — capability injection as typed WIT contracts (link time, no ambient
  effect). Heavyweight (lattice), but the linking model is a blueprint.
- **Extism** — plugin framework, **its Go SDK uses wazero (CGo-free)** — a direct precedent.
  Capability = manifest. **Footgun:** `allowed_hosts: null` = ALL hosts; no signing/registry.

### OpenClaw forks (isolation, but no out-of-band HITL)
| Fork | Stack | Isolation | Weakness |
|---|---|---|---|
| **NanoClaw** (`nanocoai/nanoclaw`) | TS, Docker | container per chat group, vault | "tamper-evident log" is a blog claim only; ambiguous name |
| **NemoClaw** (`NVIDIA/NemoClaw`) | TS CLI + Python | **real:** OpenShell (Landlock+seccomp+netns) + YAML policy | sandbox ≠ VM; approval local/policy |
| **ZeroClaw** (`elev8tion/zeroclaw`) | Rust | gateway pairing (OTP+bearer), deny-by-default channel allowlist | "<5 MB" = one macOS run, no methodology; fragmented |
| **PicoClaw** (`sipeed/picoclaw`) | **Go**, single binary, MCP | multi-arch | **no** capability/security model; ~95% agent-generated |
| **nanobot** (`HKUDS/nanobot`) | Python + MCP | "safer workspace access" | **no** sandbox/gate/approval; name collides |

### HITL players (in-app/desktop, not out-of-band)
- **Cline** (~58k★) — per-tool approval, auto-approve toggles, `requires_approval`, shadow-git
  checkpoints. VS Code in-editor, no sandbox, not out-of-band.
- **QwenPaw** (AgentScope) — "tool guard" YAML + `ShellEvasionGuardian`, levels
  STRICT/SMART/AUTO/OFF, kernel sandbox, skill scanner. Local, **no** push.
- **Goose** (Block, Rust) — modes autonomous/manual/smart/chat-only, per-tool always/ask/never.
  In-app.
- **OpenHands** (Python) — confirmation mode (`WAITING_FOR_CONFIRMATION`), cleanly separated
  `SecurityAnalyzer` (LLM tags risk) vs `ConfirmationPolicy`. Coding, in-app.
- **MS Agent Framework** — per-function `approval_mode`; a run returns `user_input_requests` and
  **the app supplies the channel** — exactly the hook a phone layer would need. No transport.
- **Shannot** (`corv89/shannot`) — HITL via syscall interception + a virtual FS, review in a
  **TUI**. Local, not mobile.

### Out-of-band / phone approval — the niche is split into 3 fragments
- **Bolt-on MCP** (`telegram-assistant-mcp`) — tiny/generic, no sandbox.
- **Claude Code hooks** (`claude-push`, `claude-ntfy-hook`) — `PermissionRequest` hook + ntfy SSE
  with allow/deny. They prove the demand, but are coding-scoped, **the topic name is the only
  auth (weak)**, un-sandboxed.
- **Enterprise OAuth CIBA** (Auth0/Okta) — the most rigorous out-of-band approval, standardized,
  but **transaction-scoped**, not a general trust boundary. *Option:* CIBA as a transport for
  standards credibility.
- **MCP elicitation** — the *right* primitive (pause a tool until user input), but
  transport-agnostic → somebody has to wire it to the phone.

### Positioning matrix
| Project | Stack | Isolation | HITL | Out-of-band | Mandatory | Ops | Maturity |
|---|---|---|---|---|---|---|---|
| **Nocturn** | Go+wazero | capability, zero ambient | gate | **yes, enforced** | **yes, not disableable** | single binary | new |
| IronClaw | Rust | WASM component+vault+fuel | grant store | no | auto-approve on | Postgres+54 crates/TEE | mature (pre-1.0) |
| OpenClaw | TS | none | iOS/Watch (exec-only) | yes, optional | **disableable** | Node gateway | very mature |
| Wassette | Wasmtime comp. | zero authority | — | no | — | MCP server | early |
| NemoClaw | TS+Rust | Landlock+seccomp+netns | policy | no | policy | kernel sandbox | new |
| Cline | TS/VS Code | none | per-action | no | optional | extension | ~58k★ |
| OpenHands | Python | Docker | confirmation | no | optional | Docker | high |

### Where Nocturn wins (and where it doesn't)
1. **Not new:** the WASM sandbox (IronClaw/Wassette) **and** phone approval (OpenClaw) exist
   separately — don't sell either as a novelty.
2. **Defensible = the combination plus the mandate:** *mandatory, non-disableable out-of-band
   approval on a separate device, at EVERY trust boundary, WASM-isolated, single binary without
   DB or cloud.* Others: *sandbox-and-automate* (IronClaw) **or** *ask-but-only-in-app*
   (Cline/OpenHands) **or** *out-of-band-but-optional-and-exec-only* (OpenClaw). Nobody makes
   out-of-band the **enforced default**.
3. **Table stakes to match:** credential vault + **bidirectional leak scanning** (otherwise
   IronClaw looks stronger).
4. **Credibility wins against IronClaw's gaps:** enforced attenuation (#5581), code signing,
   SECURITY.md + a tested audit sink (#6000/#5640), strong crypto on the approval channel.
5. **Framing:** *"IronClaw-grade tool isolation, but with enforced human consent at trust
   boundaries — on a second device, not disableable, in a single Go binary without cloud."*

## B — OpenClaw gap analysis (threat → our answer)

OpenClaw's architecture: **channel** (messaging adapters) → **brain** (loop, memory) → **body**
(tools: browser/shell/cron). Skills = plain files via "ClawHub", model-agnostic.

| # | Documented weakness | Root cause | Nocturn's answer |
|---|---|---|---|
| 1 | Prompt injection (~57% robustness); web/message content hijacks the agent | LLM output drives privileged tools directly | gate + **HITL for irreversible/external**; LLM output is *untrusted* |
| 2 | **Exfil via link previews** (PromptArmor): the agent builds an attacker URL, the preview fetches it | unthrottled egress | no ambient network; egress gated + **leak scanning** + HITL for new targets |
| 3 | Malicious skills / ClawHub supply chain (Cisco) | skills unsandboxed with host rights | **WASM zero authority**; skills signed + **attenuation enforced** |
| 4 | Weak default configs (CNCERT) → takeover | ambient rights, opt-out | **deny by default**: no capability without an explicit grant |
| 5 | Exposed control UI/dashboards | network-reachable web UI | **local-only**, paired devices, no open listener |
| 6 | Irreversible mistakes (MoltMatch) | no approval gate for destructive ops | HITL mandatory for send/delete/pay/commit |
| 7 | Governance / "vibe slop" | usability over security | small audited core; append-only audit; SECURITY.md |

**Core:** two independent threat classes → two defenses. Malicious *code* (#3,#4) → **WASM
sandbox**. Injection abusing *legitimate* tools (#1,#2,#6) → **gate + out-of-band HITL** (the
sandbox alone does not stop injection; in-band approval sits in the same trust domain → hence a
**separate device**).

## C · D — Runtime evaluation & trust boundary → `ADRS.md`

Moved to the decision records: **wazero vs Wasmtime** (WASIp1-only, no component model or fuel —
why it is still right), **PORTICO** (capability revocation), and **trust boundary variant A**
(the loop in the host, skills/tools in WASM). See `ADRS.md`.

## E — Roadmap M0–M7

**Status:** M0–M5 stand; the greenfield rebuild on agentkit replaced the hand-rolled loop and
broker with the engine + gate split.

- **M0 scaffold** ✅ — wazero runs a zero-authority guest.
- **M1 broker + first host fn** ✅ — now `agentkit/gate`: allow/ask/deny with remembered grants.
- **M2 signed + attenuated skills** ⬜ — Ed25519, enforced attenuation, example skill.
- **M3 out-of-band HITL** ✅ — approval broker + paired mobile app + APNs wake, not disableable.
- **M4 vault + leak scan** ✅ — host-owned credential injection, bidirectional leak scan.
  Open: keychain ⬜, single-use/zeroize ⬜ (Go language limit, best-effort later).
- **M5 the loop** ✅ — now agentkit (zero-dep, policy-blind), gated by agentkit/gate.
- **M6 companion app** ✅ — shipped as the mobile app (Angular + Capacitor, iOS).
- **M7 policy + hardening** 🔷 — metrics/SECURITY.md/audit sink ⬜, security pass.

## F — Security proof checklist (what we must always be able to demonstrate)

- **Zero authority:** a guest without a grant can open no connection and no FS (link/trap error). ✅
- **Gate precedence:** deny beats a narrower allow; unknown action → deny; a grant covers only
  its own target. ✅
- **Attenuation (M2):** an installed skill demonstrably cannot write/HTTP/shell (the counter to
  IronClaw #5581). ⬜
- **Out-of-band HITL E2E:** approve ⇒ action + audit; deny/timeout ⇒ trapped; the boundary policy
  is not disableable (negative test). ✅
- **Vault/leak scan (M4):** a secret is never in guest memory; egress carrying a secret → blocked;
  ingress secret → flagged/redacted. ✅
- **Exfil regression (OpenClaw #2):** egress to a non-granted target → blocked; new domain → "ask"
  instead of silent execution. ✅
- **Hardening (M7):** OOM/deadline guests trapped cleanly; the daemon exposes only the paired
  WebSocket. 🔷

## G — Open points

**Open:** skill guest language (TinyGo vs Rust) · CIBA adoption · escape hatch to a
wasmtime-go/component backend · keychain backend · the skill PDK fork (Extism's 34-module TCB vs
our own host + Javy — pillar 2 DevEx vs pillar 4 small TCB) · when agentkit moves to its own repo.
