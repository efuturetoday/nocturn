<div align="center">

<img src="assets/mascot.png" alt="The Nocturn mascot — a sleeping gopher in a nightcap and sleep mask, drifting through a starfield." width="220">

# Nocturn

**A personal AI assistant that works on its own — and stops for your approval, on your phone,
before it does anything you can't take back.**

One Go binary. No cloud, no database, no runtime to install.

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![CI](https://github.com/efuturetoday/nocturn/actions/workflows/ci.yml/badge.svg)](https://github.com/efuturetoday/nocturn/actions/workflows/ci.yml)
[![CGO](https://img.shields.io/badge/CGO-free-success)](#)
[![Engine dependencies](https://img.shields.io/badge/engine%20dependencies-0-success)](agentkit/go.mod)

</div>

---

## Why this exists

I wanted agents doing real work on my own life — mail, calendars, files, services I actually pay
for. Every way to do that today asks for the same thing first: hand a cloud assistant your
credentials and your data, and trust the sandbox.

That trade is the wrong shape. The value of a personal assistant is that it reaches your real
accounts; the risk is exactly the same fact. So Nocturn is built so the model never holds the
credential, and never takes an irreversible step without a human saying yes — on a device the
attacker isn't standing in.

Which is where the name comes from. The agents run at night, unattended, on a schedule. The point
is being able to sleep through it.

## The two threats, and why one wall can't stop both

For a security product the threat model *is* the product. Nocturn's design falls out of one
observation: an assistant faces two independent threats that need two different defenses.

**1. Malicious code.** A skill or plugin you install is hostile, or a good one is compromised
upstream. Running with your privileges, it reads your disk and opens sockets directly.
→ **The sandbox.** Untrusted code runs in WebAssembly at *zero* authority. Capability it was not
handed is capability it cannot name. This isolates the code.

**2. Prompt injection.** The model reads a web page, an email, a message, and that content carries
an instruction. No malicious code is involved at all — the injection rides on tools you granted on
purpose, and its goal is almost always to **exfiltrate**.
→ **Starve it, then gate it.** The model never holds your secrets, so there is nothing to hand
over. Everything it *can* still call goes through a gate, and anything outbound or irreversible
waits for an out-of-band yes.

The tempting shortcut — "just sandbox everything" — does not work. The sandbox cannot stop
injection, because the injection uses the very tools the sandboxed code is *allowed* to call: the
call is authorized, the intent is not. And in-band approval cannot stop it either. **A prompt that
appears in the session the injection already captured can be answered by the injection.** Consent
has to come from somewhere it cannot reach. That is why the second device is mandatory rather than
a convenience.

## Architecture

```
   user turn (terminal REPL · mobile app over WebSocket · spoken, through a satellite)
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

The repository is two halves that are deliberately not allowed to blur:

**`agentkit/` — the engine.** Its own Go module whose `go.mod` has **no `require` block at all**.
An LLM-agnostic turn loop, immutable tool and skill sets, sub-agents (a sub-agent is just a tool),
one-way event streaming, per-turn and per-tree guards. Everything external is a port — `LLM`,
`Tool`, `Logger`, `Store`. The core is **policy-blind**: it knows nothing about permissions, because
gating belongs in a wrapper on top, never inside the component the model's output flows through.
Provider adapters (`openai`, `gemini`), the permission layer (`gate`), ready tools and the
composition root are sibling modules.

**`internal/` — Nocturn.** The security boundary the engine deliberately has no opinion about: the
wazero sandbox, the encrypted vault and its boundary injector, the gated tools, out-of-band
approval, transcript persistence, and composition per workspace.

Two axes of control are kept apart on purpose: **which** tools an agent has at all is a `ToolSet`,
bound once and statically. **What** a tool may *do* is the gate — evaluated per action, asked when
risky, remembered at the scope the human picked.

---

## 🧠 Development methodology: an agentic SDLC

This project is also a case study in AI-driven engineering, and the interesting part is not that an
agent wrote code. It is that **the process was compiled into the repository** rather than promised
in a README.

`.claude/settings.json` is committed, and it holds two hooks that every contributor — human or
model — runs into:

```jsonc
// PreToolUse — a commit with unreviewed Go in the index is DENIED, not warned about.
"matcher": "Bash", "if": "Bash(git commit*)"
//   → staged *.go files are hashed; unless that exact diff has been through the Go
//     review skills (Effective Go + the Google Go Style Guide, cited by rule and file:line),
//     the commit does not happen. A stamp over the diff hash means the same content
//     is never blocked twice.

// PostToolUse — a commit touching only internal/ or cmd/ BLOCKS afterwards
//   → until the affected documentation is updated, or the omission is justified in writing.
```

That is the difference between *"I reviewed carefully"* and *review being a precondition for a
commit existing at all*. Every commit in this history passed both gates. The same idea runs through
`docs/AGENTS.md`, whose one rule is **"this site documents the code in this repository, not a design
of it"**, and through `CLAUDE.md` §6, a running list of pitfalls actually hit — so a mistake is paid
for once.

**The division of labour.** Architecture, security boundaries, the threat model, the ADRs, and
review were mine. Implementation, tests, documentation and the mechanical work happened inside those
constraints. The constraints came first and are in the repository as artifacts — `ADRS.md` is eleven
decision records, most written before the code they govern.

**What that produced**, all of it checkable from a clone:

| | |
|---|---|
| **381 commits** in 23 days, **374** carrying a `Co-Authored-By: Claude` trailer | `git log` |
| **22,037** lines of Go — against **23,758** lines of test | `git ls-files '*.go' \| xargs wc -l` |
| **809** test functions, the whole suite green under `-race` | `go test -race ./...` |
| **`agentkit/gate` at 100 %** statement coverage — the permission layer | `go test -cover` |
| sandbox 90.9 % · secret 90.4 % · hitl 91.4 % · agentkit 92.9 % · auth 93.2 % | `go test -cover` |
| **Zero** third-party dependencies in the engine | [`agentkit/go.mod`](agentkit/go.mod) |
| CGO-free, ~18 MB, six targets from one source tree | [CI](.github/workflows/ci.yml) |
| 4,014 lines of C (ESP32 firmware) · 4,834 lines of Angular/iOS app | one protocol, three clients |

The honest caveat: this pace is only available because the constraints were unusually explicit. The
ADRs, the pitfalls file and the two hooks are not documentation *about* the work — they are the
input that made the work converge.

---

## What runs today

| Tool | Gate |
|---|---|
| `http_read` `http_write` `dns_resolve` `ping` | `net`, target = host |
| `file_read` `file_list` `file_stat` `file_search` `file_write` `file_remove` `file_move` | `file`, target = path, workspace-confined |
| `notify` | `notify` |
| `remind` `remind_list` `remind_cancel` | `remind` |
| `memory_write` | `memory` — allowed in chat, asked in unattended agent runs |
| `memory_read` `skill_read` `time_now` `wake` `whoami` | **ungated** — context, never authority |
| `code_run` (JavaScript on QuickJS-in-WASM) | woven per cage, so a script's reach *is* its cage |

Plus **remote MCP** servers (HTTPS only — a local stdio server would be a foreign process with your
rights), **sandboxed plugins** whose manifest is reviewed without running the artifact, and
**agentskills.io skills** read from disk.

A workspace is the portable unit — no database, the folder *is* the state:

```
nocturn-data/workspaces/main/
  mnt/           ← the ONLY thing the model sees: file-tool root + sandbox /work
  PERSONA.md     ← the assistant's system prompt          ┐
  agents/        ← child-agent declarations               │ control plane:
  skills/        ← procedural knowledge                   │ outside the mount,
  memory/        ← durable notes, catalog folded per turn │ so the model can
  grants.json    ← remembered permissions                 │ neither read nor
  vault.enc      ← credentials, this workspace's own key  ┘ write them
  chats/ agent-runs/
```

The control-plane split is **structural, not a deny rule**: those paths are simply not in the mount.
Self-modification is solved by construction rather than by a check that could be wrong.

## Quickstart

```bash
go build ./cmd/nocturn        # or grab a release binary
cp .env.example .env          # OPENAI_BASE_URL / _MODEL / _API_KEY — any OpenAI-compatible endpoint

./nocturn                     # terminal chat
./nocturn serve               # the WebSocket daemon the mobile app and satellites talk to
```

Nocturn ships no model; point it at one you control. Everything it knows lives in a `nocturn-data/`
folder next to wherever you ran it. Full setup, including the encrypted vault and pairing a phone,
is in [Getting started](https://nocturn.dev/guides/getting-started/).

```bash
go test -race ./...           # 809 tests
cd docs && npx astro build    # the docs site, schema-validated
```

## Status

**Built and tested:** the engine and its gate, the WASM sandbox, the secret vault with boundary
injection and bidirectional leak scanning, out-of-band approvals over WebSocket and APNs, remote
MCP, plugins, skills, memory, scheduled agents, the iOS companion app.

**Experimental — expect it to move:** spoken sessions (`internal/voice`, a live-audio model) and
speaker recognition (`internal/speaker` + `internal/onnx`, a pure-Go ONNX subset with no CGO,
running a WeSpeaker ResNet34). Recognition is 100 % top-1 among 2–6 enrolled voices in the measured
household set, and it chooses **context and address, never permission** — speech is a channel like
the chat, where nobody authenticates the typist either. The browser client is still labelled a PoC
harness in its own help text.

**Next:** retrieval over a workspace's documents (an `Embedder` port with a remote adapter, hybrid
semantic + lexical search, exposed as one tool); **a hosted push relay**, so waking a phone stops
requiring your own Apple Developer account — safe to hand off precisely because a push carries no
authority and no content; **an Android app**, which the Angular-under-Capacitor choice makes a build
target rather than a rewrite; extracting `agentkit` into its own repository; Ed25519 signing for
skills and plugins; a keychain backend for the vault.

**Deliberately not built:** an ambient `exec` tool. See [ADR-6](ADRS.md) — every step toward a
general coding agent erodes the one thing this design is for.

## Reading further

- **[Documentation](https://nocturn.dev)** — guides, the gate reference, the threat model
- **[ADRS.md](ADRS.md)** — eleven decision records, and the reasoning behind each
- **[agentkit/DOCS.md](agentkit/DOCS.md)** — the engine's design, ports, and composition model
- **[CLAUDE.md](CLAUDE.md)** — how to work in this repository, and the pitfalls already paid for
- **[CONTRIBUTING.md](CONTRIBUTING.md)** · **[SECURITY.md](SECURITY.md)**

## License

[Apache-2.0](LICENSE).
