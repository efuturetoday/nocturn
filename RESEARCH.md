# Competitive research

> External research, not derivable from this repo — that is why it is kept. It describes **other
> projects** and was accurate when gathered; nothing here is checked against our tree, and `★`
> counts and version numbers are rough (fetch summarizers confabulate numbers — verify via the
> GitHub API before quoting them anywhere public).
>
> For our own current state see **`CLAUDE.md` §1–4 + git log**.

## IronClaw (`nearai/ironclaw`) — the direct rival, the benchmark

Rust reimplementation of OpenClaw, 54-crate workspace, Apache-2.0/MIT, ~12.5k★, v0.29.1
(June 2026, pre-1.0). Illia Polosukhin (NEAR) associated. Hosted = NEAR AI Cloud (TEE).

- **Isolation:** Wasmtime 46, component model + WASI Preview 2. WASM tools see only **4 host
  functions** (`log`, `now_unix_secs`, `workspace_read/write`); network via a host-side HTTP proxy.
  Per-tool `capabilities.json`. Memory limit via `ResourceLimiter`, **CPU via fuel** (100M instr).
  Pipeline: `WASM→allowlist→leak scan→credential→execute→leak scan→WASM`.
- **Strengths (do not attack head-on):** (a) **credential vault** `ironclaw_secrets`: AES-256-GCM,
  per-secret HKDF-SHA256, domain-separated AAD, low-entropy guard, secret never in WASM memory —
  the table-stakes model. (b) **Bidirectional leak scanning** (15+ patterns, in and out).
  (c) Rust memory safety + mature component isolation + fuel. (d) Feature breadth (MCP client,
  many providers/channels, NL→WASM tool autogen, hybrid vector search).
- **Weaknesses (verified, beatable):** (a) **No out-of-band/phone HITL** (`phone|twilio|push|sms`
  = 0 hits). "Approval" = a persistent grant store `ironclaw_approvals` (allow/ask/deny) that does
  **not prompt the user**, with **auto-approve on by default**; "ask" is *in-band* (attackable).
  Background triggers reach no human — the system only *denies* high severity.
  (b) `PolicyAction::Review` = **stub**. (c) **Skill attenuation not ported** (#5581), capability
  catalog leaks (#5712), **no code signing**. (d) **No SECURITY.md**, private vuln reporting off
  (#6000). (e) Multi-tenant leak (#5460), audit sink gaps (#5640/#5428). (f) **Heavyweight:**
  Postgres 15 + pgvector + 54 crates + Node/pnpm web UI; "simple" = NEAR cloud/TEE lock-in. A
  "reborn" rewrite is underway → the design is unsettled.

## OpenClaw itself — the HITL incumbent (surprisingly)

- iOS app + **Apple Watch** review and approve pending **`exec` requests** from the phone
  (`operator.approvals`, "first committed answer wins"). Real out-of-band approval, already shipped.
- **Weaknesses (our counter):** only `tools.exec`, not all boundaries · **disableable** ("never
  stop on exec approvals") · TS monolith, no WASM sandbox.

### OpenClaw gap analysis (threat → our answer)

Its architecture: **channel** (messaging adapters) → **brain** (loop, memory) → **body** (tools:
browser/shell/cron). Skills = plain files via "ClawHub", model-agnostic.

| # | Documented weakness | Root cause | Our answer |
|---|---|---|---|
| 1 | Prompt injection (~57% robustness); web/message content hijacks the agent | LLM output drives privileged tools directly | gate + approval for irreversible/external; LLM output is untrusted |
| 2 | **Exfil via link previews** (PromptArmor): the agent builds an attacker URL, the preview fetches it | unthrottled egress | no ambient network; egress gated + leak scanning + ask for new targets |
| 3 | Malicious skills / ClawHub supply chain (Cisco) | skills unsandboxed with host rights | WASM zero authority; signing + attenuation (open) |
| 4 | Weak default configs (CNCERT) → takeover | ambient rights, opt-out | no capability without an explicit grant |
| 5 | Exposed control UI/dashboards | network-reachable web UI | paired devices only, no open listener |
| 6 | Irreversible mistakes (MoltMatch) | no approval gate for destructive ops | approval mandatory for send/delete/pay/commit |
| 7 | Governance / "vibe slop" | usability over security | small audited core; audit sink + SECURITY.md (open) |

## WASM sandbox tech (isolation competition)

- **MS Wassette** — an MCP server running WASI components as tools (Wasmtime). WIT→JSON schema
  automapping, OCI distribution **signed (Notation/Cosign)**, deny-by-default, YAML policy. The
  cleanest standards-conformant capability injection, but "not production ready", Rust+Wasmtime+CGo,
  **no HITL**.
- **wasmCloud/Cosmonic** — capability injection as typed WIT contracts (link time, no ambient
  effect). Heavyweight (lattice), but the linking model is a blueprint.
- **Extism** — plugin framework; its Go SDK uses wazero (CGo-free) — a direct precedent.
  Capability = manifest. **Footgun:** `allowed_hosts: null` = ALL hosts; no signing/registry.

## OpenClaw forks (isolation, but no out-of-band HITL)

| Fork | Stack | Isolation | Weakness |
|---|---|---|---|
| **NanoClaw** (`nanocoai/nanoclaw`) | TS, Docker | container per chat group, vault | "tamper-evident log" is a blog claim only |
| **NemoClaw** (`NVIDIA/NemoClaw`) | TS CLI + Python | real: OpenShell (Landlock+seccomp+netns) + YAML policy | sandbox ≠ VM; approval local/policy |
| **ZeroClaw** (`elev8tion/zeroclaw`) | Rust | gateway pairing (OTP+bearer), deny-by-default channel allowlist | "<5 MB" = one macOS run, no methodology |
| **PicoClaw** (`sipeed/picoclaw`) | Go, single binary, MCP | multi-arch | no capability/security model; ~95% agent-generated |
| **nanobot** (`HKUDS/nanobot`) | Python + MCP | "safer workspace access" | no sandbox/gate/approval |

## HITL players (in-app/desktop, not out-of-band)

- **Cline** (~58k★) — per-tool approval, auto-approve toggles, `requires_approval`, shadow-git
  checkpoints. VS Code in-editor, no sandbox.
- **QwenPaw** (AgentScope) — "tool guard" YAML + `ShellEvasionGuardian`, levels
  STRICT/SMART/AUTO/OFF, kernel sandbox, skill scanner. Local, no push.
- **Goose** (Block, Rust) — modes autonomous/manual/smart/chat-only, per-tool always/ask/never.
- **OpenHands** (Python) — confirmation mode (`WAITING_FOR_CONFIRMATION`), cleanly separated
  `SecurityAnalyzer` (LLM tags risk) vs `ConfirmationPolicy`. Coding, in-app.
- **MS Agent Framework** — per-function `approval_mode`; a run returns `user_input_requests` and
  **the app supplies the channel** — exactly the hook a phone layer needs. No transport of its own.
- **Shannot** (`corv89/shannot`) — HITL via syscall interception + a virtual FS, review in a TUI.

## Out-of-band / phone approval — the niche is split into 3 fragments

- **Bolt-on MCP** (`telegram-assistant-mcp`) — tiny/generic, no sandbox.
- **Claude Code hooks** (`claude-push`, `claude-ntfy-hook`) — `PermissionRequest` hook + ntfy SSE
  with allow/deny. They prove the demand, but are coding-scoped, the topic name is the only auth
  (weak), and nothing is sandboxed.
- **Enterprise OAuth CIBA** (Auth0/Okta) — the most rigorous out-of-band approval, standardized,
  but transaction-scoped, not a general trust boundary. *Option:* CIBA as a transport for
  standards credibility.
- **MCP elicitation** — the right primitive (pause a tool until user input), but
  transport-agnostic → somebody has to wire it to the phone.

## Positioning matrix

| Project | Stack | Isolation | HITL | Out-of-band | Mandatory | Ops | Maturity |
|---|---|---|---|---|---|---|---|
| **Nocturn** | Go+wazero | capability, zero ambient | gate | **yes** | **yes** | single binary | new |
| IronClaw | Rust | WASM component+vault+fuel | grant store | no | auto-approve on | Postgres+54 crates/TEE | mature (pre-1.0) |
| OpenClaw | TS | none | iOS/Watch (exec-only) | yes, optional | disableable | Node gateway | very mature |
| Wassette | Wasmtime comp. | zero authority | — | no | — | MCP server | early |
| NemoClaw | TS+Rust | Landlock+seccomp+netns | policy | no | policy | kernel sandbox | new |
| Cline | TS/VS Code | none | per-action | no | optional | extension | ~58k★ |
| OpenHands | Python | Docker | confirmation | no | optional | Docker | high |

## Where Nocturn wins (and where it doesn't)

1. **Not new:** the WASM sandbox (IronClaw/Wassette) and phone approval (OpenClaw) each exist —
   don't sell either as a novelty.
2. **Defensible = the combination plus the mandate:** mandatory, non-disableable out-of-band
   approval on a separate device, at every trust boundary, WASM-isolated, single binary without DB
   or cloud. Others: sandbox-and-automate (IronClaw), or ask-only-in-app (Cline/OpenHands), or
   out-of-band-but-optional-and-exec-only (OpenClaw). Nobody makes out-of-band the enforced default.
3. **Table stakes to match:** credential vault + bidirectional leak scanning.
4. **Credibility wins against IronClaw's gaps:** enforced attenuation (#5581), code signing,
   SECURITY.md + a tested audit sink (#6000/#5640), strong crypto on the approval channel.
5. **Framing:** *"IronClaw-grade tool isolation, but with enforced human consent at trust
   boundaries — on a second device, not disableable, in a single Go binary without cloud."*

## Security proof checklist

What we should always be able to demonstrate. Status is a snapshot — re-verify before quoting it.

- **Zero authority:** a guest without a grant can open no connection and no FS.
- **Gate precedence:** deny beats a narrower allow; unknown action → deny; a grant covers only its
  own target.
- **Attenuation:** an installed skill demonstrably cannot write/HTTP/shell (the counter to
  IronClaw #5581). — open, see CLAUDE.md §9.
- **Out-of-band approval E2E:** approve ⇒ action + audit; deny/timeout ⇒ fail closed; an
  unattended agent's fresh ask is denied.
- **Vault/leak scan:** a secret is never in guest memory; egress carrying a secret → blocked;
  ingress secret → redacted.
- **Exfil regression (OpenClaw #2):** egress to a non-granted host → ask, not silent execution.
- **Hardening:** OOM/deadline guests trapped cleanly; the daemon exposes only the paired
  WebSocket. — audit sink and metrics still open.
