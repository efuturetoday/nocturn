# Nocturn — Architecture Decision Records

> The *why* behind the design — the choices and their rationale, which no `go doc`
> can tell you. Package-level *what* lives in the code (`go doc ./internal/<pkg>`,
> `go doc ./agentkit/...`); project vision, patterns, and pitfalls live in `CLAUDE.md`.
> This file is the decision log: read it before reversing a decision, and add one when
> you make a new load-bearing choice.

Status legend: **decided** (still just a decision) · **realized in `<pkg>`** (built).

---

## ADR-1 — One isolation gate: WASM/wazero
No second in-process interpreter (goja) for foreign code — a second interpreter means no memory
isolation and a second security door to keep straight (sprawl). Polyglot comes from compile
stages; JS/TS → QuickJS-in-WASM. **Code execution is first-class**; a pure compute transformation
needs **zero permissions**. *Realized in `sandbox` + `script`.*

## ADR-2 — Native effects are host tools, not guest code
WASM cannot exec binaries. Rebuild the common effects (`http`, `dns`, `ping`) **natively in Go**;
real `exec` is the last resort only (allowlist + HITL + OS sandbox). The model calls those tools
directly — the same gated tools the script interpreter and plugins reach through. *Realized in
`internal/tools` (`exec` deliberately absent — see ADR-7).*

## ADR-3 — Distribution borrowed from IronHub, simplified
Git monorepo + `index.json` (url + sha256) + release assets, **no OCI**. Tool (wasm) / skill
(Markdown) split. **Nocturn-plus: code signing** (IronClaw has none). *Decided.*

## ADR-4 — Dynamic target-gating, not a static allowlist
Known target → auto-allow; unknown → **mandatory out-of-band HITL**. Rigid per-tool allowlists
cannot express "ask about the unknown": the risky part of a call is its *target* (which host,
which path), and that only exists at call time. So the unit of decision is an **action**
(`Action{Kind, Target}`) evaluated per call, not a tool listed once at startup. `Kind` is a tool
name *or a shared axis* — `http_read`, `http_write`, `ping` and `dns_resolve` all gate on
`"net"`, so one grant covers the whole axis instead of one per tool. An answer is remembered at
the scope the human picked — this session, or always. *Realized in `agentkit/gate` (policy →
allow/ask/deny, grants, approver) + the target-matching tools in `internal/tools`.*

## ADR-5 — Host-managed credentials/OAuth; the guest never sees the token
The host runs the OAuth flow + refresh and injects the Bearer only at the boundary; the guest gets
only presence (a secret *exists*), never the value. *Realized in `secret` + `secret/oauth`.*

## LLM provider — go-openai + native tool_calls
**go-openai** (dependency-free) for the chat call; **native `tool_calls`** (confirmed live against
freellm) instead of a parsed prompt protocol; arguments JSON-Schema-validated (unmarshal + retry
on error). Since ADR-11 this is the *only* place the dependency exists: go-openai is an indirect
dep of the tree, reachable through one adapter module and nothing else. *Realized in
`agentkit/openai`.*

## ADR-6 — Product identity: a secure personal assistant, NOT a coding agent
The defensible moat is the *combination* — mandatory out-of-band HITL + WASM isolation +
per-action gating + single binary — which no one has. Every step toward a full coding agent
(ambient `exec`, local MCP servers, sandboxing a Node/Python runtime) **erodes exactly that moat**
and makes Nocturn a worse Claude Code. So: **assistant-first by default.** Coding navigation
(grep/read/edit/git plumbing) is still covered — without `exec`. "Whatever coding agents use" is
the wrong yardstick. Not irreversible: the `exec` escape hatch (ADR-7) can be added later without
flipping the default. *Decided.*

## ADR-7 — Tool taxonomy: the 3-bucket compass
Classify by *what the tool does*, not "it's a CLI":
- **(A) local read/compute** (grep/find/ls/jq/text) → **`code_run`** (the model writes JS in the
  QuickJS sandbox and reads through `file_read`, **zero new permissions**) or the `file_search`
  tool.
- **(B) API-client "CLIs"** (gh, aws, gcloud, stripe, linear, curl) → **a plugin over
  `http_read`/`http_write` + a host-injected token** — *safer* than the real CLI (token host-held,
  cage-bounded, HITL on writes); the model's sweet spot.
- **(C) real arbitrary exec** (npm test, go build, make) → the **only `exec` escape hatch**
  (OS sandbox + allowlist + HITL, ADR-2's "last resort"), **never the default**.

A + B cover the overwhelming majority without `exec`. *A + B realized (`script`, the `file_*` and
network tools in `internal/tools`, `plugin`); C deliberately unbuilt.*

## ADR-8 — Kernel-vs-plugin boundary: "expressible through the primitives? → plugin"
The **host stays a minimal kernel**: the gate + HITL + interpreter (`code_run`) + the **primitives
that need a real syscall** (`http`, `file`, `dns` — a guest cannot open a socket or the FS
itself). **Everything expressible through those primitives → a plugin** (git = `file` on `/work`
+ `http` for push/pull; gmail/github = `http`). Putting trusted first-party code in a plugin buys
*no* isolation, but keeps the **TCB small** (pillar 4) + one uniform extension model +
signable/versionable — which outweighs the sandbox overhead. **git concretely: go-git built
`GOOS=wasip1 GOARCH=wasm` as `plugin.wasm`** (Go does this natively; the go-git dep lives in the
plugin build, never the host). **NOT** `wasm-git`/libgit2 (Emscripten↔wazero break) and **no
CGo**. Costs accepted: WASI-FS is slower, the plugin writes its own HTTP transport, local git
FS-ops run over the confined `/work` mount (not per-op HITL) — only the push/commit gate is
brokered. *Decided; `plugin` realized.*

## ADR-9 — MCP line: remote (HTTP) YES, local (stdio) NO
*Local* stdio-MCP = a foreign **process on your machine** with your rights → exactly the
supply-chain threat we avoid. *Remote* MCP = a service on **someone else's infra**; no code runs
locally → architecturally identical to "call an HTTP API" and fits the model exactly (MCP client
as JSON-RPC-over-HTTP: `tools/list` → tool specs, `tools/call` → a gated network action against
the MCP host, OAuth host-injected, HITL on writes, results leak-scanned/untrusted). **No sandbox
needed (no foreign code).** Opens the growing **hosted-MCP ecosystem** (GitHub, Notion, Linear,
Sentry, Atlassian…) *without* breaking the model — and that is where MCP is heading. Reminder: the
*largest* assistant ecosystem is Markdown skills (5,400+), not MCP — adopted un-sandboxed-safe
because a skill only acts through gated tools. *Realized in `mcp` (+ `mcp/authflow`).*

## ADR-10 — Workspace = the portable/versionable unit; the LLM inhabits only `mnt/`
No DB — the **workspace folder IS the state** (data + skills + standing rights), copyable/git-able
as a whole. Layout:
```
nocturn-data/
  devices.json         ← paired devices, process-wide (not per workspace)
  workspaces/main/     ← "main" is DefaultWorkspace
    mnt/               ← the ONLY thing the LLM sees: file-tool root + sandbox /work (data plane)
    PERSONA.md         ← the assistant's system prompt (control plane, optional)
    agents/            ← child-agent declarations (host-read, not mounted)
    skills/            ← procedural knowledge (host-read, not mounted)
    plugins/  mcp/     ← installed extensions; each may hold its own secrets.enc shard
    grants.json        ← host-managed standing permissions, outside the mount
    vault.enc          ← encrypted credentials (this workspace's own key), outside the mount
    bindings.json      ← host-owned credential bindings
    reminders.json     ← pending reminders
    chats/  agent-runs/ ← persisted transcripts (user chats · agent firings)
```
**The control-plane/data-plane split is structural (mount scope), not a deny rule:** the model can
neither see nor write `agents`/`skills`/`grants`/`PERSONA.md`, because they are simply not in the
mount — confinement by construction. The self-modification threat is solved for free. **Severity
clarity:** a self-written *skill* grants no authority (the gate reads no skills, `allowed-tools`
is ignored) → low; the **load-bearing** protection is `grants.json` (authority-granting — if the
model could write it, an injection could set itself standing grants → HITL silent → gate
bypassed). So `grants.json` lives in the workspace (per-workspace, portable), not `~/.config`.
*Realized in `workspace` (composition + the grant store it owns) + the workspace-confined `file_*`
tools.*

## ADR-11 — agentkit is a separate, zero-dependency, policy-blind module
The turn loop, the ports, the immutable sets, sub-agents, events and guards are **product-
independent**; nocturn's security boundary is not. Holding both in one package made the engine
un-publishable and blurred which half the security actually lives in. So the engine is its own
module (`agentkit`, joined via `go.work`), and nocturn is one consumer of it.

Three constraints keep the split honest:
- **Zero dependencies in the core.** Every provider, transport and storage concern must be a port
  (`LLM`, `Tool`, `Logger`, `Store`); the adapters (`gate`, `runtime`, `openai`, `tools`) are
  sibling modules, not internals. A dep in the core would mean a concern leaked inward.
- **The core is policy-blind.** It knows nothing about permissions; gating is a wrapper on top
  (ADR-4). Rejected alternative: a Gate/Decision type inside the loop — it would tie every future
  consumer to nocturn's permission model and put the security decision inside the component that
  the LLM's output flows through. The split also forces two questions apart that get conflated:
  **WHICH** tools an agent has at all (`ToolSet.Select`, bound once, statically) vs **WHAT** a tool
  may do (the gate, per action, asked when risky, remembered).
- **A sub-agent is a tool**, not a subsystem: `AgentTool` wraps an agent as a tool whose call runs
  a nested session. Nesting rides on ctx (event frame, shared budget), so a single set of guards
  caps the whole tree instead of each level re-arming its own.

*Realized in `agentkit` + `agentkit/{gate,runtime,openai,tools}`; extraction into its own
repository is still open (CLAUDE.md §9).*

## ADR-12 — Retrieval: documents in the mount, the index outside it, embeddings remote
The corpus lives at `mnt/knowledge/` — INSIDE the file tools' mount — and that is the mirror image
of ADR-10's treatment of memory. Memory is control plane: its catalog reaches every future prompt,
so it sits outside the mount where no generic file tool can rewrite it. Documents are **data**: they
enter the prompt only when a tool goes looking, and putting one there grants nobody anything — the
same argument ADR-10 makes about a self-written skill. The **index** does not follow them in. It is
host state (hashes, offsets, vectors), and a model that could edit it could point a search result at
text that is not in the file, so it sits beside `grants.json`.

**`knowledge_search` is ungated**, on the argument that already leaves `memory_read` and `skill_read`
ungated: context, never authority, reaching nothing `file_read` could not already reach. The one
thing worth stating rather than glossing is that answering EMBEDS the query, which sends it to the
configured provider — host configuration with the same standing as the endpoint already reading
every message, not a target the model chose. The decision is made once, by configuring an embedder.

**What comes back never claims an author.** Because the corpus is in the mount, `file_write` — and
therefore a prompt injection — can put a document there. Introducing that to the model as "the
user's own note" would launder exactly the attack ADR-4 and the out-of-band gate exist to stop, so
results are framed as quoted file content, explicitly not as instructions and explicitly not as
something the user wrote.

**The embedder is a port with a REMOTE adapter, and that is a concession.** Indexing sends every
document to a third party. A local model would remove the trade; `internal/onnx` runs a
convolutional speaker network in pure Go, and a sentence-transformer needs a transformer's operator
set plus a tokenizer, which is a project rather than a slice. The honest position is the port, a
remote adapter behind it, the leak scanner in front of it, and documentation that says so. The
**document reader is a port for the same reason** — PDF, Office and image extraction each need a
dependency that has no business inside a package the whole workspace links.

**Hybrid search, fused by rank.** Vectors miss exact identifiers; keywords miss paraphrase. Scores
from the two share no scale, so reciprocal rank fusion uses only the order each produced. *Realized
in `knowledge` (+ `knowledge/embed`).*

---

## Trust boundary — Variant A: loop in the host, plugins/skills in WASM
**Chosen** and current. Alternative B (loop + plugins both in WASM) kept open.

| | A: loop in host (**chosen**) | B: loop + plugins in WASM |
|---|---|---|
| LLM keys | never in the sandbox | must be brokered |
| Isolation | foreign code isolated; gate + HITL against the *effect* of injection | the loop is isolated too |
| Complexity | low, idiomatic Go | high (loop + LLM I/O over the ABI) |
| Injection defense | **identical** (comes from gate + HITL) | identical |

**Rationale:** injection defense comes from **per-action gating + out-of-band HITL**, not from
where the loop runs. A is simpler, same net security, and keeps keys out of the sandbox. Build the
host-function boundary so `agentkit.Session` could later move to B without a rewrite.

## wazero over Wasmtime — honest runtime placement
wazero is **WASIp1-only, no component model** (#2289 "not planned"), **no fuel**. Costs vs.
Wasmtime: no typed WIT interfaces / no WIT→tool mapping (we supply our own manifest/schema layer,
Extism-style); coarser WASIp1; CPU bounded only via **context deadline + memory-page cap**;
component tools from Wassette/wasmCloud won't run without a shim. **Why still right:** a CGo-free
**single binary** (trivial cross-compile), and **every host import = your Go function** → the
boundary is maximally auditable, wrappable, revocable. Escape hatch: keep the host-import
interfaces abstract enough for a later wasmtime-go/component backend.
