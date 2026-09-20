# Architecture

> Three files, three jobs: **this** one is the map and the cross-cutting facts, **`ADRS.md`** is the
> decisions and their reasons, and the **package doc comment** is the mechanism
> (`go doc ./internal/<pkg>`, `go doc ./agentkit[/<mod>]`) — the doc comments here are good, use them.

## What Nocturn is

A secure personal AI assistant in Go — a single binary, no foreign runtime, orchestrating an LLM
through permission-gated, human-approved tools.

The defensible angle: *mandatory out-of-band approval on a second device, WASM-isolated, in a single
Go binary without DB or cloud* (ADR-6).

Two threat classes, two defenses:

| Threat | Defense |
|---|---|
| malicious plugin/skill **code** | the WASM sandbox — isolates the code (ADR-1) |
| prompt injection abusing **legitimate** tools | the gate + out-of-band approval — isolates the effect (ADR-4) |

The ask is rendered from `gate.Action{Kind, Target}`, never from anything the model wrote, and the
runs it exists for are unattended.

## The two halves

**`agentkit/` — the engine.** Its own module; `go.mod` has no `require` block at all. An LLM-agnostic
turn loop, immutable tool/skill sets, sub-agents (a sub-agent is a tool), a one-way event stream,
per-turn and per-tree guards. Everything external is a port (`LLM`, `Tool`, `Logger`, `Store`). The
core is policy-blind. Sibling modules: `gate`, `runtime`, `tools`, `openai`, `gemini` (ADR-11).
Destined for its own repository — nothing nocturn-specific may leak in.

**`internal/` — nocturn.** The security boundary (sandbox, secrets, gated tools), what agentkit leaves
to its consumer (transcript persistence, skill sources, discovery), and composition per workspace.

```
cmd/nocturn        the binary: process spine, workspace open, `serve` daemon
internal/…         the packages below
agentkit/…         the engine + gate/runtime/openai/tools/gemini
mobile/            the companion app (Angular + Capacitor, iOS) — the second device
docs/              the docs site (Astro/Starlight); tool/capability data is schema-validated
catalog/extensions/<name>/  the published catalog's SOURCE — one folder per installable thing,
                   holding exactly what an install writes, plus entry.json and extension.sig.
                   Generated into docs/public/catalog.json (committed, CI-drift-checked)
sdk/_template/     the starting point for a plugin (manifest + JS + TS source)
media/             Remotion explainers — outside go.work, never linked (ADR-14)
```

## Request flow

```
   user turn (the terminal UI, or the mobile app over WebSocket)
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

## Package index

One line each — `go doc` the package for what it really does.

**agentkit (separate module)**

| Package | |
|---|---|
| `agentkit` | loop, ports, sets, sub-agents, events, guards, pausable budget |
| `gate` | Policy→Ruling, Grants with recall, Approver, `Check`/`Wrap` |
| `runtime` | wires LLM+tools+skills+gate into ready-to-run sessions |
| `openai`, `gemini` | LLM adapters; go-openai exists only in `openai` |
| `tools` | generic gated tools: `HTTPGet`, host matching |

**Security boundary**

| Package | |
|---|---|
| `sandbox` | wazero guest at zero authority + WASI + brokered imports + memory cap + wall-clock deadline |
| `secret` (+`/oauth`) | store (a guest learns a secret exists, never its value), encrypted vault, host-owned injector, bidirectional leak scanner |
| `auth` | device registry; bearers stored only as sha256 hashes, constant-time compared |
| `hitl` | out-of-band approval broker implementing `gate.Approver` |
| `push` | APNs; a push is a WAKE, never a decision |

**Tools & extensions**

| Package | |
|---|---|
| `tools` | the thin gated tools |
| `script` | untrusted JS on QuickJS/wasm; exactly ONE host import, `nocturn.call`, plus WASI |
| `plugin` | sandboxed plugins; the manifest declares tools/`uses` cage/credentials and is reviewed WITHOUT running the artifact |
| `mcp` (+`/authflow`) | stdlib-only protocol client over an injected transport + the gated connection layer (ADR-9) |

**Context & composition**

| Package | |
|---|---|
| `memory` | the assistant's durable notes; catalog derived from the notes on disk and folded into every prompt, bodies on demand. Control plane, one writer |
| `frontmatter` | the shared `---` YAML preamble parser/renderer: skills and memory notes |
| `extension` | ONE tree, `extensions/<name>/`, and nothing else is installable — see below |
| `skill` | agentskills.io skills from the extensions tree → `agentkit.SkillSet`, with `{{config.x}}` substituted from the extension's config; unconfigured = still listed, body replaced by what to run |
| `discovery` | the shared name/skip rules for agents, skills, plugins, MCP (a skill names itself in SKILL.md) |
| `knowledge` (+`/embed`) | retrieval over `mnt/knowledge`: Markdown-aware chunking behind a `Reader` port, an `Embedder` port, hybrid cosine+BM25 fused by reciprocal rank, index outside the mount, one-minute reconcile (ADR-12) |
| `mail` | the household's mailbox over `go-imap/v2` — not an extension, one per workspace, own folder `mail/` (ADR-17) |
| `chat` | file-backed transcript store + Manager |
| `agent` | declaration + cron only; execution is injected by the workspace |
| `workspace` | the composition root, cut in two by what may not exist twice (ADR-15): `Open` builds the durable half, `Reload` swaps the derived `snapshot` behind an atomic pointer. `Registry` is the daemon's set of open workspaces; its `OnOpen` hangs the per-workspace wiring |
| `serve` | WebSocket surface, tagged JSON, one file per domain |
| `library` | the curated catalog extensions are installed from: daemon-wide, lazy, ONE configured host over TLS with every payload inline; `DefaultURL` is what an unset `NOCTURN_CATALOG_URL` means, `off` means no library |
| `webui` | the browser front-end, `go:embed`ded: the SAME Angular bundle `mobile/` ships, copied in by `generate.sh`, gitignored but for a `.gitkeep`. A file server and nothing else; absent bundle = a 503 page naming the build command |
| `tui` (+`/transcript`, `/logring`) | the terminal surface, `serve`'s sibling (ADR-13). `transcript` is the pure event→blocks fold, the Go port of mobile `chat-model.ts`, pinned by a convergence test |
| `speaker` | who spoke: Kaldi-compatible filterbank → embedding → cosine, `voices.json` per workspace. Chooses context and address, never permission. The threshold belongs to a CHANNEL: 0.50 close-talking, 0.45 satellite, measured — `internal/speaker/testdata/README.md` |
| `onnx` | the inference engine `speaker` runs on: a narrow ONNX subset in pure Go, no CGO |

### The workspace on disk

The folder IS the state — no DB, copyable and git-able as a whole (ADR-10). The model sees `mnt/` and
nothing else; the confinement is the mount scope, not a deny rule.

```
nocturn-data/
  devices.json         ← paired devices, process-wide (not per workspace)
  workspaces/main/     ← "main" is DefaultWorkspace; the FOLDER NAME is the identity (ADR-16)
    mnt/               ← the ONLY thing the LLM sees: file-tool root + sandbox /work (data plane)
    PERSONA.md         ← the assistant's system prompt (control plane, optional)
    agents/            ← child-agent declarations (host-read, not mounted)
    extensions/<name>/ ← everything installed, one folder each (below)
    mail/              ← mail.json + its own shard. Not an extension; a folder because a shard is
                         keyed by its PATH, so the passwords go when the mailbox goes
    grants.json        ← standing permissions, outside the mount, 0600 via a temp file
    vault.enc          ← encrypted credentials (this workspace's own key), outside the mount
    reminders.json     ← pending reminders
    chats/  agent-runs/ ← persisted transcripts (user chats · agent firings)
    .trash/            ← a removed workspace is MOVED here; the registry's scan skips dot-dirs
```

**Never rename a workspace folder.** It is the input to the vault key and to every shard key — see
`.agents/docs/pitfalls.md`.

### `extension` — the one installable shape

`extensions/<name>/` carries whatever its files say: `SKILL.md` text · `plugin.json`+`plugin.js` code ·
`mcp.json` a remote server — and a folder may carry several (a household installs "Home Assistant",
not three things sharing a name). Beside the payloads: `manifest.json` (the declaration — typed
`config` a human supplies, `credentials` the host injects), `config.json` (what they typed),
`secrets.enc` (its own shard).

Owner is `ext:<name>`, credential key `ext:<name>@<host>/<cred>` — re-point it at another host and the
old token is not found. `CredentialDecl.Audience`: the zero value is the extension itself, `model`
additionally the model's own calls, which is what a SKILL needs.

## The tools that exist today

`internal/tools`, plus the heavy ones in their own packages.

| Tool | Gate |
|---|---|
| `http_read` `http_write` `dns_resolve` `ping` | `NetKind`, target = host |
| `file_read` `file_list` `file_stat` `file_search` `file_write` `file_remove` `file_move` | `FileKind`, target = path, workspace-confined |
| `notify` | `NotifyKind` |
| `remind` `remind_list` `remind_cancel` | `RemindKind` |
| `memory_write` (`internal/memory`) | `memory.Kind`, target = note path, outside `mnt`; allowed in chat, asked in agent runs |
| `memory_read` (`internal/memory`) | ungated — context, never authority |
| `mail_send` (`internal/mail`) | `mail.SendKind`, target = recipient address, one check per address, asks in both policies; widening `*@domain` |
| `mail_list` `mail_search` `mail_read` | ungated; reading PEEKS; registered only with a `mail.json` |
| `time_now` `wake` | ungated — zero authority, `wake` bounded |
| `whoami` (`internal/speaker`) | ungated; registered only when `NOCTURN_SPEAKER_MODEL` is set |
| `knowledge_search` (`internal/knowledge`) | ungated; registered only when an embedding endpoint is configured |
| `code_run` (`internal/script`) | woven per cage by `tools.Compose` — a script's reach is its cage |
| `skill_read` (`internal/skill`), `skill_load` (agentkit) | context, never authority |

## Dependencies

agentkit core: **none**. nocturn: `wazero`, `coder/websocket`, `libp2p/zeroconf/v2`, `x/crypto`,
`x/net`, `x/oauth2`, `aho-corasick` (leak scanner only), `yaml.v3` (frontmatter only),
`lmittmann/tint`, `godotenv`, `grindlemire/go-tui` (pre-1.0, PINNED; only runtime dep is `x/sys`),
`emersion/go-imap/v2` + `go-message` (pre-1.0 beta, PINNED; they bring `go-sasl` and `x/text`).
`go-openai` is indirect.

Rejected: langchaingo (290 deps, brings its own loop that bypasses our security).

Dev tools: `wat2wasm` (brew wabt) for the WAT test guests; `wasi-sdk` + a quickjs-ng checkout to
rebuild the interpreter wasm; `go-tui/cmd/tui` for the `.gsx` templates — deliberately NOT a `go tool`
directive, which would drag `x/tools`, `x/mod` and `x/sync` into `go.mod` for a generator that never
ships.
