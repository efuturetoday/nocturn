# Nocturn — Architecture Decision Records

> **A record here is a decision, not a manual.** Problem, decision, the reason that carries it, what
> was rejected — and a pointer to where the mechanism lives. If a paragraph explains *how* something
> works, it belongs in `.agents/docs/` or in the package's doc comment, not here.
>
> Read this before reversing a decision. Add one when you make a new load-bearing choice.

Status: **decided** (still just a decision) · **realized in `<pkg>`** (built).

---

## ADR-1 — One isolation gate: WASM/wazero
*Realized in `sandbox` + `script`.*

**Problem.** Foreign code has to run; each runtime that can run it is a security door to keep straight.
**Decision.** WASM/wazero is the only one. Polyglot comes from compile stages (JS/TS → QuickJS-in-WASM).
Code execution is first-class, and a pure compute transformation needs zero permissions.
**Why.** A second in-process interpreter (goja) means no memory isolation and a second door — sprawl.
**Detail:** `go doc ./internal/sandbox`, `go doc ./internal/script`.

## ADR-2 — Native effects are host tools, not guest code
*Realized in `internal/tools`; `exec` deliberately absent (ADR-7).*

**Problem.** WASM cannot exec binaries, and the effects an assistant needs are exactly the ones that
touch the OS.
**Decision.** Rebuild the common effects natively in Go (`http`, `dns`, `ping`, `file`). The model
calls them directly — the same gated tools the script interpreter and plugins reach through.
**Why.** One implementation, one gate, one audit point, whoever is calling.
**Detail:** `.agents/docs/architecture.md` (the tool table).

## ADR-3 — Distribution borrowed from IronHub, simplified
*Decided; realized for the catalog in `library` (see ADR-19).*

**Decision.** Git monorepo + an index of url + sha256 + release assets. No OCI. Tool (wasm) / skill
(Markdown) split, plus code signing, which IronHub has none of.

## ADR-4 — Dynamic target-gating, not a static allowlist
*Realized in `agentkit/gate` + the target-matching tools in `internal/tools`.*

**Problem.** The risky part of a call is its *target* — which host, which path — and that only exists
at call time. A per-tool allowlist cannot express "ask about the unknown".
**Decision.** The unit of decision is an **action**, `Action{Kind, Target}`, evaluated per call. Known
target → auto-allow; unknown → mandatory out-of-band HITL. An answer is remembered at the scope the
human picked: this session, or always.
**Why.** `Kind` is a tool name *or a shared axis* — `http_read`, `http_write`, `ping` and `dns_resolve`
all gate on `"net"` — so one grant covers an axis instead of one grant per tool.
**Detail:** `.agents/docs/permissions.md`, `go doc ./agentkit/gate`.

## ADR-5 — Host-managed credentials; the guest never sees the token
*Realized in `secret` + `secret/oauth`.*

**Decision.** The host runs the OAuth flow and refresh and injects the Bearer at the boundary. The
guest learns only that a secret *exists*.
**Why.** A value the guest can read is a value the guest can exfiltrate; presence is all it needs to
decide whether a call is possible.
**Detail:** `.agents/docs/permissions.md` (credentials), ADR-20 (lifetime and ownership).

## ADR-6 — Product identity: a secure personal assistant, NOT a coding agent
*Decided.*

**Decision.** Assistant-first by default. Coding navigation (grep/read/edit/git plumbing) is covered —
without `exec`.
**Why.** The moat is the *combination*: mandatory out-of-band HITL + WASM isolation + per-action gating
+ single binary. Every step toward a full coding agent (ambient `exec`, local MCP servers, sandboxing a
Node/Python runtime) erodes exactly that and makes Nocturn a worse Claude Code. "Whatever coding agents
use" is the wrong yardstick.
**Not irreversible.** The `exec` escape hatch (ADR-7) can be added later without flipping the default.

## ADR-7 — Tool taxonomy: the 3-bucket compass
*A + B realized (`script`, `internal/tools`, `plugin`); C deliberately unbuilt.*

**Decision.** Classify by what a tool *does*, not by "it's a CLI":

| | | |
|---|---|---|
| **A** | local read/compute (grep/find/ls/jq/text) | `code_run` in the QuickJS sandbox, or `file_search` — **zero new permissions** |
| **B** | API-client "CLIs" (gh, aws, stripe, curl) | a plugin over `http_read`/`http_write` + a host-injected token |
| **C** | real arbitrary exec (npm test, go build) | the **only** `exec` escape hatch — OS sandbox + allowlist + HITL, never the default |

**Why.** A + B cover the overwhelming majority without `exec`, and B is *safer* than the real CLI: the
token is host-held, the cage bounds the call, writes are gated.

## ADR-8 — Kernel-vs-plugin boundary: expressible through the primitives? → plugin
*Decided; `plugin` realized.*

**Decision.** The host stays a minimal kernel: gate + HITL + interpreter + the primitives that need a
real syscall (`http`, `file`, `dns`). Everything expressible through those is a plugin — git is `file`
on `/work` plus `http` for push/pull; gmail and github are `http`.
**Why.** Trusted first-party code in a plugin buys no isolation, but keeps the TCB small, gives one
uniform extension model, and makes the thing signable and versionable. That outweighs the sandbox
overhead.
**Rejected.** `wasm-git`/libgit2 (Emscripten↔wazero break) and any CGo. git would be go-git built
`GOOS=wasip1 GOARCH=wasm`.
**Costs accepted.** WASI-FS is slower; a plugin writes its own HTTP transport; local git FS-ops run over
the confined `/work` mount rather than per-op HITL.

## ADR-9 — MCP line: remote (HTTP) YES, local (stdio) NO
*Realized in `mcp` (+ `mcp/authflow`).*

**Decision.** Remote MCP servers only.
**Why.** Local stdio-MCP is a foreign **process on your machine with your rights** — the supply-chain
threat we exist to avoid. A remote server runs no code locally, so it is architecturally identical to
"call an HTTP API" and needs no sandbox: `tools/list` for specs, `tools/call` as a gated network action,
OAuth host-injected, results leak-scanned and untrusted. It also opens the hosted-MCP ecosystem without
bending the model.
**Worth remembering.** The largest assistant ecosystem is Markdown skills, not MCP — adopted safely
un-sandboxed because a skill acts only through gated tools (ADR-10).
**Detail:** `go doc ./internal/mcp`.

## ADR-10 — The workspace is the portable unit; the LLM inhabits only `mnt/`
*Realized in `workspace` + the workspace-confined `file_*` tools.*

**Problem.** State needs somewhere to live, and the model needs somewhere it cannot reach.
**Decision.** No DB — the workspace folder IS the state, copyable and git-able as a whole. `mnt/` is the
only thing the LLM sees; agents, extensions, `grants.json`, `PERSONA.md` and the vault sit outside it.
**Why.** The control-plane/data-plane split is **structural — mount scope, not a deny rule**. The model
cannot write what is not in the mount, so self-modification is solved by construction. Severity is not
uniform: a self-written *skill body* grants no authority (the gate reads no skills, `allowed-tools` is
ignored), while `grants.json` is load-bearing — a model that could write it could grant itself standing
permissions and silence HITL. That is why grants live in the workspace, not `~/.config`.
**Where "a skill grants no authority" stops.** A `manifest.json` is not text: it tells the host to stamp
a stored secret onto every request to a host it names, ambiently (ADR-20, ADR-19).
**Detail:** `.agents/docs/architecture.md` (the workspace layout), `.agents/docs/permissions.md`.

## ADR-11 — agentkit is a separate, zero-dependency, policy-blind module
*Realized in `agentkit` + `agentkit/{gate,runtime,openai,tools}`; extraction into its own repository is
still open.*

**Problem.** Engine and security boundary in one package made the engine un-publishable and blurred
which half the security lives in.
**Decision.** The engine is its own module; nocturn is one consumer. Three constraints keep the split
honest: **zero dependencies in the core** (every provider, transport and storage concern is a port),
**the core is policy-blind** (gating is a wrapper, ADR-4), and **a sub-agent is a tool**, not a
subsystem.
**Why the policy-blindness specifically.** A Gate/Decision type inside the loop would tie every future
consumer to nocturn's permission model and put the security decision inside the component the LLM's
output flows through. It also keeps two questions apart that get conflated: WHICH tools an agent has
(`ToolSet.Select`, static) vs WHAT a tool may do (the gate, per action).
**Detail:** `agentkit/DOCS.md`, `.agents/docs/architecture.md`.

## ADR-12 — Retrieval: documents in the mount, the index outside it, embeddings remote
*Realized in `knowledge` (+ `knowledge/embed`).*

**Decision.** The corpus lives at `mnt/knowledge/`; the index does not follow it in.
**Why.** Documents are *data* — they enter the prompt only when a tool goes looking, and putting one
there grants nobody anything (ADR-10's argument about a skill body). The index is host state, and a
model that could edit it could point a search result at text that is not in the file. So the index sits
beside `grants.json`.
**`knowledge_search` is ungated** on the argument that already leaves `memory_read` and `skill_read`
ungated: context, never authority, reaching nothing `file_read` could not. The one thing worth stating
is that answering EMBEDS the query — host configuration with the same standing as the endpoint already
reading every message, decided once by configuring an embedder.
**What comes back never claims an author.** `file_write`, and therefore an injection, can put a document
in the mount. Results are framed as quoted file content, explicitly not instructions and explicitly not
something the user wrote.
**The remote embedder is a conceded trade.** Indexing sends documents to a third party. A local model
would remove it, but a sentence-transformer needs a transformer's operator set plus a tokenizer — a
project, not a slice (`internal/onnx` runs a convolutional network, which is a different size of
problem). The honest position is a port, a remote adapter behind it, the leak scanner in front of it,
and saying so. The document reader is a port for the same reason.
**Detail:** `go doc ./internal/knowledge` (chunking, hybrid cosine+BM25 fused by reciprocal rank
because the two scores share no scale).

## ADR-13 — The terminal is a full-screen surface, and it OWNS the screen
*Realized in `tui` (+ `tui/transcript`, `tui/logring`).*

**Problem.** The REPL printed the conversation to stdout while slog printed diagnostics to stderr; on
one terminal the two interleave — an answer with a timestamp glued into it, an approval prompt scrolled
away. Running the chat at WARN was not a fix but a decision to stop looking.
**Decision.** `internal/tui` is a full-screen alternate-screen app and the only writer to the screen.
Diagnostics go to a file and an in-memory ring the log pane opens on. It refuses to start without a TTY
rather than degrading.
**Why the shape holds.** It is `serve`'s **sibling**, not its replacement: both sit on the `workspace`
facade, both fold the same event stream, neither knows the other exists — which is what keeps the
terminal from growing a second, quieter model of a chat. And the fold is a **package, not a renderer**:
`tui/transcript` is a deliberate port of the mobile client's `chat-model.ts`, pinned by a convergence
test, because two clients folding one stream is where drift is silent and expensive.
**Two consequences worth recording.** `Ctrl+C` cancels the turn and does not quit — in a full-screen UI
"stop what you are doing" and "kill the program" are plainly different requests. And a terminal approval
has **no timeout**, unlike `hitl`'s two minutes: out of band nobody may be looking, here somebody is,
and a deadline would only refuse what the reader was still reading.
**Detail:** `.agents/docs/workflow.md` (keys), `.agents/docs/pitfalls.md` (go-tui and `.gsx`).

## ADR-14 — The explainers are rendered from code (Remotion), outside the binary
*Decided; `media/`.*

**Problem.** What is hardest to explain is not the structure but the *order*: a turn arrives, the model
asks, `gate.Check` turns that into an action, the ask leaves the machine, a human answers elsewhere, and
only then does the effect happen. Static pictures say the nouns and drop the verbs.
**Decision.** `media/` is a Remotion project rendered to video by CI: its own `package.json`, outside
`go.work`, never imported, never linked. Rendered files are not committed.
**Why.** The source is text — reviewable in a pull request, diffable, fixable by editing one line. A
timeline editor would make the explainers the one part of this repository that cannot be reviewed. A
composition can also read the schema `docs/` already validates against, so an explainer cannot quietly
name tools that do not exist.
**Why outside the build.** Nocturn's identity is a single Go binary with no foreign runtime; a React
project in the tree reads as a breach of that until someone says otherwise. The claim is about what
ships, and no explainer ships.
**Licensing is a headcount question.** Remotion's Free License covers up to three people; from four it
is a paid Company License, and open source does not enter into it. Re-check when the set of people who
render changes, not when the code does.

## ADR-15 — A workspace is cut in two by what may not exist twice
*Realized in `internal/workspace/{workspace,snapshot,lifecycle}.go`, `agentkit/session.go`.*

**Problem.** Discovery ran once at `Open`, so adding a skill or an MCP server meant restarting the
daemon — untenable the moment a phone can add one. Reopening does not work in either order: close-then-
open leaves seconds with no workspace, open-then-close puts two vaults on one `vault.enc`.
**Decision.** Split by what may not exist twice. **Durable** (one vault handle, one timer per reminder,
one chat store, one index, one injector) is built once by `Open`. **Derived** (agents, skills, plugins,
MCP servers, the toolset, the per-agent runtimes) is a `snapshot`, published with one atomic store.
**Why one pointer, not a field per concern.** `Inventory` reads the tool list and the MCP list together;
two guarded fields let a reader land between the writes and report a workspace that never existed. One
swap makes that unrepresentable, and a failed rebuild leaves the previous snapshot standing for free.
**The turn, not the session, is where a new snapshot takes effect.** An open conversation sees a new
skill in its next message; a turn already running keeps the set it was handed, because the model plans
against a tool list and a tool must not vanish between two calls it decided to make together. **Agent
runs are the deliberate exception** — a run keeps the cage it fired under, since widening an authority
boundary mid-run, unattended, is the one place "immediately" is the wrong answer.
**Rejected.** A filesystem watcher (a dependency, recursive watch management, events dropped under load
— and a periodic reconcile needed anyway as the backstop; `internal/knowledge` made this call first) and
a ticker (it would re-run every MCP handshake against other people's servers on a schedule). So a reload
is asked for.

## ADR-16 — A workspace's folder name is its identity; its title is a label over it
*Realized in `internal/workspace/{meta,registry}.go`.*

**Problem.** A workspace shows a name on a screen and somebody will want to change it. Renaming the
folder is the obvious implementation and it destroys credentials **silently** — the folder name is the
input to the vault key and to every shard key, so a rename makes them undecryptable with no error until
something reaches for a token.
**Decision.** Identity is the folder, permanently: the key input, the `ws` field on every wire command,
the `ws=` on every log line. The title lives in `workspace.json` and changes freely.
**Rejected.** A stable id in `workspace.json` with keys derived from it. Renaming would become trivial
and correct, and key derivation would then depend on a mutable file *inside* the thing it protects. What
renaming buys is a prettier directory name.
**Deletion follows.** The folder is every conversation, every note and a vault, and it is being removed
from a list on a phone — so it is **moved** to `.trash/<name>-<unix>`. A dot-directory, because the
registry's scan skips those.

## ADR-17 — Mail: reading is context, sending is its own Kind aimed at the recipient
*Decided; realized in `mail`.*

**Decision.** Reading is ungated. Sending gates on `mail.SendKind` with the **recipient address** as
Target, one check per address, and asks in the base policy too.
**Why reading is ungated.** Same reading as `memory_read` and `knowledge_search`: context, never
authority. Asking permission to look into one's own inbox buys nothing — the risk of a mail is not that
it was read but that it is *foreign text*, the plainest injection channel in the tree, defended where
the injection would have its effect plus ingress redaction.
**Why the recipient is the Target.** The Target of a net action is the host, and the host of an SMTP
submission is one's own provider: a remembered yes for `smtp.provider.de` would cover every future
message to everyone. This deviates from `FileKind`, where read and write share one Kind, and the
deviation is the point — there a path bounds both directions, here `mail · chef@firma.de` would not tell
a person whether something is being read or sent.
**Why it asks in the base policy.** The argument that an interactive transcript makes approval "before
instead of after" holds for a note on disk and collapses for a mail: there is no after.
**Dependencies.** `emersion/go-imap/v2` + `go-message`, pinned, `net/smtp` for submission. Rejected: our
own client on the `internal/mcp` precedent — the hard part of mail is not the protocol but MIME, which
we would have had to borrow anyway, leaving the difficult half foreign and the easy half hand-written.
**Not folded into the knowledge index.** `knowledge_search` is ungated because the corpus is what the
household itself filed. A mailbox is the opposite: anyone who knows the address can write to it, so
indexing it hands every sender a write into the retrieval corpus — surfaced at a moment the attacker
picks, with rank fusion erasing which corpus a hit came from. Keeping an index current would also need UIDVALIDITY, deletions and moves — a sync
engine, against a directory walk — while server-side `SEARCH` costs one command and copies nothing. Open door: a mail-specific index with its own provenance, never fused into one ranking.
**Detail:** `go doc ./internal/mail` (the pooled IMAP connection, per-message SMTP, the two vault
entries, why the username stays out of the vault), `.agents/docs/permissions.md`.

## ADR-18 — A device class is a fact about a device, never a value on the wire
*Realized in `internal/serve/{capabilities,origin}.go`, `internal/auth`.*

**Decision.** `manage` is a **capability, not a gated action**. A class is **derived** from what a holder
already sends, interpreted in one function, and never transmitted.
**Why manage is not gated.** The gate exists for the model acting under smuggled instructions, judged by
a human on a device the injection cannot reach. A device adding a workspace is the opposite shape — a
human command from an authenticated device — and routing it through the broker would ask the phone to
confirm what the phone just tapped. The precedent is `device.forget`.
**Why the class is not on the wire.** A value the client controls is not a fact about the client.
**Why the origin check needs the host check first.** Comparing `Origin` to `r.Host` is self-referential,
since whoever owns a DNS name owns both. That bypass was *measured* before `hostOK` existed. So the Host
must be un-rebindable first — an IP literal, `localhost`, or `.local` — and a real hostname, being
indistinguishable from the attack, is named once by configuration.
**Why every TTL needs a second chance.** The bootstrap code was armed once for five minutes and missing
it had no exit but deleting `devices.json`. One bit could not tell "join is the way in" from "there is
no way in", so `daemon.json` carries two.
**A browser is still not a second device** for the unattended case: no push provider carries one.
**Detail:** `.agents/docs/permissions.md` (classes, capabilities, the origin rules).

## ADR-19 — Text needs TLS, code needs a key, and the signature covers the pitch
*Realized in `internal/library`, `internal/serve`.*

**Decision.** Instructions are carried by one configured TLS host with every payload inline. Code is
additionally refused unless an Ed25519 signature verifies over identity, the digest of every payload,
**the listing** and **a serial**. The requirement is tied to the SOURCE: a file or loopback needs none, a
remote catalog does; a signature that is present must verify either way.
**Why the listing signs too.** A person picks by it, so a taken-over host could otherwise rebrand a
signed mail plugin as "calendar sync, no mail access" while the artifacts stayed ours.
**Why the serial signs too.** A signature says "we published these bytes", never "this is current".
Without something monotonic, a withdrawn entry can be served forever.
**Why source, not content, decides.** A signature substitutes for a channel, and a file on this machine
is not a channel — whoever can write it can drop the folder into `extensions/` directly.
**What it deliberately does not rest on.** A person reading a body first: nobody spots a subtle
instruction in four thousand tokens on a phone. The controls that hold are ADR-10 and the gate on the
first call.
**What it does not cover.** What the manifest *asks for* — `uses`, `credentials`, `oauth`. That triple is
the review surface a client must show.
**Two consequences.** The fetch is host egress (no model output flows into it, so no gate) and is lazy,
which is what lets it be on by default. And `library.install` names an entry, never carries one — a wire
form with a skill body would be a way to put arbitrary text into every system prompt of every turn.
**Known limits.** First sight of a plugin has nothing to compare a serial against; a plugin *removed*
from a catalog would need a signature over the set to detect, which would put the key in the path of
every publish.
**Detail:** `.agents/docs/permissions.md`, `internal/library/freshness.go`.

## ADR-20 — A credential belongs to what declared it, and dies with it
*Realized in `internal/extension`, `internal/workspace/extensions.go`, `internal/secret`.*

**Problem.** A workspace-wide `bindings.json` and `NOCTURN_SECRET_*` env vars both bound a credential to
a host nobody had declared, and both outlived whatever used them.
**Decision.** One declaration shape, one registration, one storage — the shard beside the folder — and
the host is *in* the key, so re-pointing an extension at another host does not find the old token.
Removing an extension revokes the remembered net grant for that host.
**Why the revocation.** A grant records what, never why. Once the thing is gone the answer stands alone,
and the next server on that host would inherit a yes nobody gave it. It cuts both ways, and being asked
once more is the cheap side of that trade.
**Why config values are validated on the way out too.** They are substituted into text the model reads
on every turn.
**Detail:** `.agents/docs/permissions.md` (the grammar, the discovery-pass ordering).

---

## Trust boundary — Variant A: loop in the host, plugins/skills in WASM
**Chosen** and current. Alternative B (loop + plugins both in WASM) kept open.

| | A: loop in host (**chosen**) | B: loop + plugins in WASM |
|---|---|---|
| LLM keys | never in the sandbox | must be brokered |
| Isolation | foreign code isolated; gate + HITL against the *effect* of injection | the loop is isolated too |
| Complexity | low, idiomatic Go | high (loop + LLM I/O over the ABI) |
| Injection defense | **identical** (comes from gate + HITL) | identical |

**Why.** Injection defense comes from per-action gating + out-of-band HITL, not from where the loop runs.
A is simpler, same net security, and keeps keys out of the sandbox. Keep the host-function boundary
abstract enough that `agentkit.Session` could move to B without a rewrite.

## wazero over Wasmtime — honest runtime placement
**Costs.** wazero is WASIp1-only, no component model (upstream: "not planned"), no fuel. So: no typed
WIT interfaces and no WIT→tool mapping (we supply our own manifest/schema layer), coarser WASIp1, CPU
bounded only by context deadline + memory-page cap, and component tools from Wassette/wasmCloud need a
shim.
**Why still right.** A CGo-free single binary, and **every host import is your Go function** — the
boundary is maximally auditable, wrappable, revocable.
**Escape hatch.** Keep the host-import interfaces abstract enough for a later wasmtime-go/component
backend.

## LLM provider — go-openai + native tool_calls
**Decision.** go-openai for the chat call, native `tool_calls` rather than a parsed prompt protocol,
arguments JSON-Schema-validated with unmarshal-and-retry on error.
**Why it stays contained.** Since ADR-11 this is the only place the dependency exists: reachable through
one adapter module and nothing else. *Realized in `agentkit/openai`.*
