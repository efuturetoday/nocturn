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

## ADR-13 — The terminal is a full-screen surface, and it OWNS the screen
The REPL printed the conversation to stdout with `fmt.Print` while slog printed diagnostics to
stderr, and on one terminal the two interleave: an answer with a timestamp glued into it, an
approval prompt scrolled away under three lines nobody asked for. The old fix was to run the chat at
WARN, which is not a fix but a decision to stop looking. The real one is to give the terminal a
single owner. `internal/tui` is a full-screen alternate-screen app and the only writer to the
screen; diagnostics go to `nocturn-data/nocturn.log` and, in parallel, to an in-memory ring the log
pane opens on. Nothing prints while it runs, which is what lets the default level go back up to
INFO. It refuses to start without a TTY rather than degrading — with the REPL gone there is no
non-interactive chat to fall back to, and escape sequences in a pipe are worse than a sentence.

**It is `serve`'s sibling, not its replacement.** Both sit on the `workspace` facade, both fold the
same `agentkit` event stream, neither knows the other exists. That symmetry is what keeps the
terminal from growing a second, quieter model of a chat.

**The fold is a package, not a renderer.** `internal/tui/transcript` turns events into blocks with
no terminal, no clock and no I/O, and it is a deliberate port of the mobile client's
`chat-model.ts` — same merge rules, same frame nesting, pinned by the same convergence test (fold a
live turn, seed the snapshot that turn persists, assert they render alike). Two clients folding one
stream is a place where drift is silent and expensive; one shape, tested from both ends, is the
answer. It also carries the whole testable surface: go-tui offers no way to drive an App against a
mock terminal, so the fold, the approver and the log ring are covered without a PTY and the
assembled layout is verified by running it.

**Ctrl+C cancels the turn; it does not quit.** The framework clears `ISIG`, so it arrives as a
keystroke, and a full-screen UI is the first place where "stop what you are doing" and "kill the
program" are plainly different requests. That also fixes something the old terminal approver got
wrong: it ignored `ctx`, so a cancelled turn left the asking goroutine blocked in `ReadString`
forever. The new one honours it.

**No timeout on a terminal approval, unlike `hitl`'s two minutes.** Out of band nobody may be
looking; here somebody is, and `gate.Check` pauses the turn's clock around the ask. A deadline would
only refuse what the reader was still reading. The option SET is the same as the broker's, in the
same order, minted from `gate.Action` alone and never from anything the model wrote — but the
broker's code is not reused: its multi-device presence, re-presentation and push wakeups have no
meaning at a keyboard. *Realized in `tui` (+ `tui/transcript`, `tui/logring`).*

## ADR-14 — The explainers are rendered from code (Remotion), and they live outside the binary
What nocturn is hardest to explain is not its structure but its *order*: a turn arrives, the model
asks for a tool, `gate.Check` turns that into an `Action{Kind, Target}`, the ask leaves the machine,
a human answers on a second device, and only then does the effect happen. Every one of those is a
box in a diagram and none of them is the point — the point is that they happen in that sequence, and
that the human sits between the ask and the effect. The same holds for the two threat classes, which
are only distinguishable by *which* defense catches them, and for zero ambient authority, which is a
statement about what the guest does not have until the host hands it over. Static pictures say the
nouns and drop the verbs.

`media/` is a Remotion project: React components, one composition per explainer, rendered to video
by CI. Chosen because the source is text — reviewable in a pull request, diffable, and correctable
by editing one line rather than by finding whoever still has the project file. A timeline editor
would make the explainers the one part of this repository that cannot be reviewed. It also lets a
composition read the same schema `docs/` already validates its tool and capability tables against,
so an explainer that names the gated tools cannot quietly fall out of step with the ones that exist.

**It is not part of the binary and not part of the Go build.** Nocturn's identity is a single Go
binary with no foreign runtime, and a React project in the tree reads as a breach of exactly that
until someone says otherwise — so: its own directory, its own `package.json`, outside `go.work`,
never imported, never linked. The claim is about what ships, and no explainer ships.

**The rendered files are not committed.** `qjs.wasm` and the generated `_gsx.go` are committed
because the build needs them; nothing needs a video. CI renders the compositions and the docs build
consumes them in the same run, which keeps tens of megabytes of re-encoded binary out of every edit
to a caption. Anchor the ignore rule at the root (`/media/out/`) — the unanchored form would match
any `out/` at any depth, which this repository has already been bitten by once.

**Licensing is a headcount question, not an open-source one.** Remotion's Free License covers
individuals and companies of up to three people; from four it requires a paid Company License, and
the project being open source does not enter into it. That is a condition on the people rendering,
so it is worth re-checking when the set of people who render changes — not when the code does.

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

## ADR-15 — A workspace is cut in two by what may not exist twice, and the turn is where the halves meet
Discovery ran once, in `workspace.Open`. Adding a skill or an MCP server meant restarting the daemon
— acceptable while the only way to add one was `mkdir` on the host, untenable the moment a phone can.

The obvious fix is to reopen the workspace, and it does not work in either order: **close then open**
leaves a window of seconds with no workspace at all (MCP handshakes are bounded at thirty seconds
each), and **open then close** puts two vaults on one `vault.enc`, two reminder sets on the same
timers, two indexes on one corpus. That failure names the real question, which is not "what survives
a reload" but **what may not exist twice**:

- **Durable** — one vault handle, one timer per reminder and per wake, one chat store, one knowledge
  index, one credential injector. Built once by `Open`, never rebuilt.
- **Derived** — agents, skills, plugins, MCP servers, the toolset they add up to, the per-agent
  runtimes. Two of these are harmless side by side, so they are a `snapshot`, built whole and
  published with one atomic store.

One pointer rather than a field per concern, because `Inventory` reads the tool list and the MCP list
together: two guarded fields let a reader land between the writes and report a workspace that never
existed. One swap makes that unrepresentable, and a failed rebuild leaves the previous snapshot
standing for free.

**The turn, not the session, is where the new one takes effect.** agentkit asks for tools, skills and
the system prompt once at the top of a turn (`WithToolsFunc` and siblings; the value forms are sugar
over them, so there is no "which wins" rule). A conversation already open sees a newly installed
skill in its very next message, while a turn already running keeps the set it was handed — the model
is given a tool list and plans against it, so a tool must not vanish between two calls it already
decided to make together. Agent runs are the deliberate exception: a run keeps the cage it fired
under, because a cage is an authority boundary and widening one mid-run, unattended, is the one place
where "pick it up immediately" is the wrong answer.

Rejected: **a filesystem watcher.** It would mean a dependency in a tree that keeps its list short,
recursive watch management for every subdirectory that appears, and platform behaviour that drops
events under load — after which a periodic reconcile is needed anyway as the backstop.
`internal/knowledge` made this call first and says so. Also rejected: **a ticker**, which would re-run
every MCP handshake against other people's servers on a schedule. So a reload is asked for:
`workspace.reload` from a device, `nocturn reload` from the terminal.

*Realized in `internal/workspace/{workspace,snapshot,lifecycle}.go`, `agentkit/session.go`.*

## ADR-16 — A workspace's folder name is its identity; its title is a label over it
A workspace shows a name on a screen and a person will want to change it. Renaming the folder is the
obvious implementation and it destroys credentials silently.

The folder name is the input to `Master.WorkspaceKey`, and to `Master.ShardKey` for every plugin and
MCP secret shard, with the workspace-relative path bound in as AAD. Renaming the directory therefore
does not move a workspace: it makes its vault and every shard undecryptable, with **no error at all**
until something reaches for a credential — the failure appears later, somewhere else, as an absent
token.

So identity is the folder, permanently: the key input, the `ws` field on every wire command, the `ws=`
on every log line, and the same rule `discovery.ResolveName` holds for plugins, MCP servers and
agents. The title lives in `workspace.json`, changes freely because nothing depends on it, and clears
back to the folder name.

Rejected: **a stable id in `workspace.json` with keys derived from it.** It would make renaming
trivial and correct, and it was still the wrong trade — key derivation would then depend on a mutable
file *inside* the thing it protects, and the workspace would be the one identity in the tree that is
not its folder, forever, in every log line and shard path. What renaming the folder buys is a prettier
directory name.

Deletion follows from the same reading: the folder is every conversation, every note and a vault, and
it is being removed from a list on a phone — so it is **moved** to `.trash/<name>-<unix>`, not
deleted. The trash is a dot-directory because the registry's scan skips those; without that skip the
next start would open the trash as a workspace, with its own vault and its own schedulers.

*Realized in `internal/workspace/{meta,registry}.go`.*

