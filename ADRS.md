# Nocturn — Architecture Decision Records

> The *why* behind the design — the choices and their rationale, which no `go doc`
> can tell you. Package-level *what* lives in the code (`go doc ./internal/<pkg>`);
> project vision, patterns, and pitfalls live in `CLAUDE.md`. This file is the
> decision log: read it before reversing a decision, and add one when you make a new
> load-bearing choice.

Status legend: **decided** (still just a decision) · **realized in `<pkg>`** (built).

---

## ADR-1 — One isolation gate: WASM/wazero
No second in-process interpreter (goja) for foreign skills — a second interpreter means
no memory isolation and a second security door to keep straight (sprawl). Polyglot comes
from compile stages; JS/TS → QuickJS-in-WASM. **Code execution is first-class**; a pure
compute transformation needs **zero capabilities**. *Realized in `sandbox` + `script`.*

## ADR-2 — Native effects are host capabilities, not guest code
WASM cannot exec binaries. Rebuild the common effects (`http`, `dns`, `ping`) **natively
in Go**; real `exec` is the last resort only (allowlist + HITL + OS sandbox). The brain
calls capabilities straight through the broker. *Realized in `netcap` (`exec` deliberately
absent — see ADR-7).*

## ADR-3 — Distribution borrowed from IronHub, simplified
Git monorepo + `index.json` (url + sha256) + release assets, **no OCI**. Tool (wasm) /
skill (Markdown) split. **Nocturn-plus: code signing** (IronClaw has none). *Decided.*

## ADR-4 — Dynamic target-gating, not a static allowlist
Known target → auto-allow; unknown → **mandatory out-of-band HITL**; plus time window +
rate. Rigid per-tool allowlists can't express "ask about the unknown." *Realized in
`capability` + `gateway`.*

## ADR-5 — Host-managed credentials/OAuth; the guest never sees the token
The host runs the OAuth flow + refresh and injects the Bearer only at the boundary; the
guest gets only presence (`secret_exists`), never the value. *Realized in `oauth` +
`secret`.*

## LLM provider — go-openai + native tool_calls
**go-openai** (dependency-free) for the chat call; **native `tool_calls`** (confirmed live
against freellm) instead of a parsed prompt protocol; arguments JSON-Schema-validated
(unmarshal + retry on error). *Realized in `llm`.*

## ADR-6 — Product identity: a secure personal assistant, NOT a coding agent
The defensible moat is the *combination* — mandatory out-of-band HITL + WASM isolation +
capability broker + single binary — which no one has. Every step toward a full coding agent
(ambient `exec`, local MCP servers, sandboxing a Node/Python runtime) **erodes exactly that
moat** and makes Nocturn a worse Claude Code. So: **assistant-first by default.** Coding
navigation (grep/read/edit/git plumbing) is still covered — without `exec`. "Whatever coding
agents use" is the wrong yardstick. Not irreversible: the `exec` escape hatch (ADR-7) can be
added later without flipping the default. *Decided.*

## ADR-7 — Tool taxonomy: the 3-bucket compass
Classify by *what the tool does*, not "it's a CLI":
- **(A) local read/compute** (grep/find/ls/jq/text) → **`code.run`** (the model writes JS in
  the QuickJS sandbox, reads via `file.read`, **zero new caps**) or a `file.search` cap.
- **(B) API-client "CLIs"** (gh, aws, gcloud, stripe, linear, curl) → **a plugin over
  `http.read/write` + host-injected token** — *safer* than the real CLI (token host-held,
  cage-bounded, HITL on writes); the model's sweet spot.
- **(C) real arbitrary exec** (npm test, go build, make) → the **only `exec` escape hatch**
  (OS sandbox + allowlist + HITL, ADR-2's "last resort"), **never the default**.

A + B cover the overwhelming majority without `exec`. *A + B realized (`script`, `filecap`,
`plugin`, `netcap`); C deliberately unbuilt.*

## ADR-8 — Kernel-vs-plugin boundary: "expressible through the primitives? → plugin"
The **host stays a minimal kernel**: broker + HITL + interpreter (`code.run`) + the
**primitives that need a real syscall** (`http`, `file`, `dns` — a guest cannot open a
socket or the FS itself). **Everything expressible through those primitives → a plugin**
(git = `file` on `/work` + `http` for push/pull; gmail/github = `http`). Putting trusted
first-party code in a plugin buys *no* isolation, but keeps the **TCB small** (pillar 4) +
one uniform extension model + signable/versionable — which outweighs the sandbox overhead.
**git concretely: go-git built `GOOS=wasip1 GOARCH=wasm` as `plugin.wasm`** (Go does this
natively; the go-git dep lives in the plugin build, never the host). **NOT** `wasm-git`/
libgit2 (Emscripten↔wazero break) and **no CGo**. Costs accepted: WASI-FS is slower, the
plugin writes its own HTTP transport, local git FS-ops run over the confined `/work` mount
(not per-op HITL) — only the push/commit gate is brokered. *Decided; `plugin` realized.*

## ADR-9 — MCP line: remote (HTTP) YES, local (stdio) NO
*Local* stdio-MCP = a foreign **process on your machine** with your rights → exactly the
supply-chain threat we avoid. *Remote* MCP = a service on **someone else's infra**; no code
runs locally → architecturally identical to "call an HTTP API" and fits the model exactly
(MCP client as JSON-RPC-over-HTTP: `tools/list` → `tool.Spec`, `tools/call` → a brokered
`http.write` to the MCP host, OAuth host-injected, HITL on writes, results leak-scanned/
untrusted). **No sandbox needed (no foreign code).** Opens the growing **hosted-MCP
ecosystem** (GitHub, Notion, Linear, Sentry, Atlassian…) *without* breaking the model — and
that is where MCP is heading. Reminder: the *largest* assistant ecosystem is Markdown skills
(5,400+), not MCP — adopted un-sandboxed-safe because a skill only acts through gated tools.
*Realized in `mcp` + `mcpcap`.*

## ADR-10 — Workspace = the portable/versionable unit; the LLM inhabits only `mnt/`
No DB — the **workspace folder IS the state** (data + skills + standing rights), copyable/
git-able as a whole. Layout:
```
workspaces/default/
  mnt/            ← the ONLY thing the LLM sees: filecap.Root + sandbox /work (data plane)
  PERSONA.md      ← the assistant's system prompt (control plane, optional; layered)
  agents/         ← child-agent declarations (host-read, not mounted)
  skills/         ← procedural knowledge (host-read, not mounted)
  grants.json     ← host-managed standing permissions, outside the mount
  secrets.vault   ← encrypted credentials, outside the mount
```
**The control-plane/data-plane split is structural (mount scope), not a deny rule:** the
model can neither see nor write `agents`/`skills`/`grants`/`PERSONA.md`, because they are
simply not in the mount — confinement by construction. The self-modification threat is solved
for free. **Severity clarity:** a self-written *skill* grants no authority (the broker reads
no skills, `allowed-tools` is ignored) → low; the **load-bearing** protection is `grants.json`
(authority-granting — if the model could write it, an injection could set itself standing
grants → HITL silent → broker bypassed). So `grants.json` lives in the workspace (per-
workspace, portable), not `~/.config`. *Realized in `workspace` (composition) + `filecap`
(mount) + `grantstore`.*

---

## Trust boundary — Variant A: brain in the host, skills/tools in WASM
**Chosen** and current. Alternative B (brain + skills both in WASM) kept open.

| | A: brain in host (**chosen**) | B: brain + skills in WASM |
|---|---|---|
| LLM keys | never in the sandbox | must be brokered |
| Isolation | skill code isolated; broker + HITL against the *effect* of injection | the loop is isolated too |
| Complexity | low, idiomatic Go | high (loop + LLM I/O over the ABI) |
| Injection defense | **identical** (comes from broker/HITL) | identical |

**Rationale:** injection defense comes from **broker + out-of-band HITL**, not from where the
loop runs. A is simpler, same net security, and keeps keys out of the sandbox. Build the
host-function boundary so the loop could later move to B without a rewrite.

## wazero over Wasmtime — honest runtime placement
wazero is **WASIp1-only, no component model** (#2289 "not planned"), **no fuel**. Costs vs.
Wasmtime: no typed WIT interfaces / no WIT→tool mapping (we supply our own manifest/schema
layer, Extism-style); coarser WASIp1; CPU bounded only via **context deadline + memory-page
cap**; component tools from Wassette/wasmCloud won't run without a shim. **Why still right:**
a CGo-free **single binary** (trivial cross-compile), and **every capability = your Go
function** → the boundary is maximally auditable, wrappable, revocable. Escape hatch: keep the
capability interfaces abstract enough for a later wasmtime-go/component backend.

## PORTICO — epoch-bound capabilities (the revocation model)
Capabilities as **epoch-bound opaque handles** instead of standing rights (arXiv 2606.22504):
bind a grant to a task/subgoal epoch, **revoke = invalidate the epoch**; every host function
is a reference monitor (stale replay refused before the effect). Attenuable Biscuit/Macaroon
tokens (narrowing only). *First step realized:* `capability.EpochRegistry` + the session scope
(`gateway.Scope`) — one epoch per session today; task/subgoal epochs are a later refinement.
