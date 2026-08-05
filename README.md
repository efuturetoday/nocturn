<div align="center">

<img src="assets/mascot.png" alt="The Nocturn mascot — a sleeping gopher in a nightcap and sleep mask, drifting through a starfield." width="220">

# Nocturn

**A personal AI assistant that works on its own — and stops for your approval, on your phone,
before it does anything you can't take back.**

One Go binary. No cloud, no database, no runtime to install.

[![Documentation](https://img.shields.io/badge/docs-nocturn-6E56CF)](https://efuturetoday.github.io/nocturn)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![CI](https://github.com/efuturetoday/nocturn/actions/workflows/ci.yml/badge.svg)](https://github.com/efuturetoday/nocturn/actions/workflows/ci.yml)
[![CGO](https://img.shields.io/badge/CGO-free-success)](#)
[![Engine dependencies](https://img.shields.io/badge/engine%20dependencies-0-success)](agentkit/go.mod)

**[📖 Read the documentation →](https://efuturetoday.github.io/nocturn)**

</div>

---

## Why this exists

I wanted an assistant that actually does things in my life — mail, calendar, files, the services I
use — and I did not want to hand an agent the run of my machine to get it.

That is the part most of them get wrong, and it is not carelessness so much as inheritance. An agent
running on Node has the filesystem, the network and the shell because its runtime does; every
capability is present by default and the only thing standing between a tool call and your disk is
whether the agent decided to make it. Isolation and human approval exist in that world, but as
features bolted on beside the runtime rather than as the thing the runtime is built around.

So I built the one I wanted:

| | |
|---|---|
| **One binary** | No Node, no Python, no container, no database. Download a file, run it. |
| **No ambient authority** | Foreign code runs in WebAssembly with nothing — no filesystem, no sockets, no clock. Every capability is an explicitly handed host function, so a capability nobody granted is not denied, it is *absent*. |
| **The model is never told about credentials** | Not the value, not the name, not that one exists. It asks for a URL; the host attaches the token as the request crosses the boundary. There is nothing about a secret in the conversation for an injection to talk it out of. |
| **Approval is the design, not a setting** | The unit of decision is **reach**, not risk: leaving the machine asks about the host, changing a file asks about the path, and your answer is remembered at the scope you pick. The yes comes from a **second device**, because a prompt shown inside a hijacked session can be answered by whatever hijacked it. |
| **The model writes code that calls tools** | `code_run` hands it JavaScript inside the same sandbox, and the calls that script makes are the same gated calls the model would have made itself — so ten files is one loop and one round trip instead of ten. Pure computation needs no permission at all; a script's reach is exactly its caller's cage, never more. |
| **A phone and a voice, first class** | A mobile app that answers approvals, and a speaker in the room you can talk to. Not integrations somebody else maintains — the same protocol, the same repository. |

The security design behind that has a real threat model, and it is
[written up properly](https://efuturetoday.github.io/nocturn/architecture/threat-model/) rather than
summarised here. The short version is that an assistant faces two independent threats — hostile code
you installed, and prompt injection riding on tools you granted on purpose — and they need two
different defenses, which is why both the sandbox and the gate exist.

Which is where the name comes from. The agents work at night, unattended, on a schedule. The point
is sleeping through it without wondering whether the mailbox is still there in the morning.

## Quickstart

```bash
go build ./cmd/nocturn        # one binary, no runtime, nothing else to install
cp .env.example .env          # OPENAI_BASE_URL / _MODEL / _API_KEY — any OpenAI-compatible endpoint

./nocturn                     # terminal chat
./nocturn serve               # the daemon your phone and your satellites talk to
```

Nocturn ships no model; point it at one you control. Everything it knows lives in a `nocturn-data/`
folder next to wherever you ran it — no database, nothing scattered through your home directory.

📱 **The companion app** is what answers an approval when you are nowhere near the machine.
A TestFlight link lands here with the public beta.

📖 **[The documentation](https://efuturetoday.github.io/nocturn)** covers the rest: full setup, the
permission model, writing plugins, connecting accounts, agents on a schedule, the wire protocol.
This page stays short on purpose — what is below is only the part you would want before deciding to
read further.

---

<table>
<tr>
<td width="50%" align="center">
<img src="assets/screenshots/app-chat-ping-allowed-then-denied.jpg" width="280" alt="One conversation: the assistant is told a name and writes it to memory, reaches google.com once, and on the second attempt reports that the action was declined.">
</td>
<td width="50%" align="center">
<img src="assets/screenshots/app-chat-list.jpg" width="280" alt="The app's chat list: every conversation in the workspace with the assistant's last line under each.">
</td>
</tr>
<tr>
<td valign="top"><b>The gate said no.</b> Same host, allowed once and refused the next time: <code>Once</code> really does mean once. A refusal stops the tool call, not the conversation — the assistant is told, and carries on.</td>
<td valign="top"><b>One workspace, many devices.</b> Conversations are not per device — this is the same workspace the terminal is talking to.</td>
</tr>
<tr>
<td width="50%" align="center">
<img src="assets/screenshots/app-tools-code-run-nested-writes.jpg" width="280" alt="The tools view of one turn: a code_run that failed, a second that succeeded with its JavaScript expanded, and the ten file_write calls it produced listed underneath with their durations.">
</td>
<td width="50%" align="center">
<img src="assets/screenshots/app-connect-discovery.jpg" width="280" alt="The app's connect screen: a daemon found on the local network by name, its WebSocket address below it.">
</td>
</tr>
<tr>
<td valign="top"><b>The model wrote a loop.</b> Every write it made surfaces as its own tool call — timed, attributable, and gated where that tool is gated. A script's reach is exactly its caller's cage. The red entry is a first attempt that failed.</td>
<td valign="top"><b>Your phone finds your daemon</b> on your own network, by name. No account, no relay, nothing in between.</td>
</tr>
</table>

## Architecture

Two halves that are deliberately not allowed to blur.

**`agentkit/` — the engine.** Its own Go module whose `go.mod` has **no `require` block at all**. An
LLM-agnostic turn loop, immutable tool and skill sets, sub-agents, event streaming, per-turn and
per-tree guards. Everything external is a port — `LLM`, `Tool`, `Logger`, `Store`. The core is
**policy-blind**: it knows nothing about permissions, because gating belongs in a wrapper on top,
never inside the component the model's output flows through.

**`internal/` — Nocturn.** The security boundary the engine has no opinion about: the wazero sandbox,
the encrypted vault and its boundary injector, the gated tools, out-of-band approval, and
composition per workspace.

Two axes of control stay apart on purpose: **which** tools an agent has at all is a `ToolSet`, bound
once and statically. **What** a tool may *do* is the gate — per action, asked when risky, remembered
at the scope you picked.

→ [Request flow](https://efuturetoday.github.io/nocturn/architecture/request-flow/) ·
[The two halves](https://efuturetoday.github.io/nocturn/architecture/agentkit/) ·
[Cage and gate](https://efuturetoday.github.io/nocturn/reference/gate/) ·
[ADRS.md](ADRS.md) — eleven decision records

---

## How this was built

**Agentic first.** Almost all of the code here was written by an AI agent. What did not come from
the agent is the part that decides what the code becomes: the **architecture**, the **security
boundaries**, the **threat model**, the **ADRs**, the **code style**, and the **review**. Agentic
pair programming, with the human on the side that gets to say no.

The constraints came first and live in the repository as artifacts rather than as habits —
[`ADRS.md`](ADRS.md) is where most decisions were written down before the code they govern.

**Where that got to**, all of it verifiable from a clone:

| | | |
|---|---|---|
| **409** commits in 23 days, **402** with a `Co-Authored-By: Claude` trailer | | `git log` |
| **24,066** lines of Go — against **25,268** lines of test | | `wc -l` |
| **864** test functions, green under `-race` | | `go test -race ./...` |
| **`agentkit/gate` 100 %** statement coverage | the permission layer itself | `go test -cover` |
| auth 94.2 % · agentkit 92.9 % · hitl 91.4 % · sandbox 90.9 % · secret 90.4 % | the parts that hold the boundary | `go test -cover` |
| **zero** third-party dependencies in the engine | no `require` block at all | [`agentkit/go.mod`](agentkit/go.mod) |
| CGO-free, ~18 MB, six targets | built on every push | [CI](.github/workflows/ci.yml) |
| 4,014 lines of C · 4,834 lines of Angular | firmware and app, one protocol | — |

Said plainly, because the number makes it obvious: at twenty-four thousand lines the review is
**best effort**. Everything load-bearing — the gate, the sandbox, the vault, the approval path — was
read closely and is where the coverage went. Every line of every test was not.

### Agentic contributions are welcome

Send them. This project would be a strange place to object.

One condition, and it is the one I hold myself to: **a human reviewed it before it was submitted.**
Not "an agent produced it and the tests pass" — somebody read the diff, understood what it does to
the boundaries above, and is willing to answer questions about it. An issue is the same: checked
against the code before it is filed, not a model's guess about what the code might do.

An unreviewed agent PR is not a contribution, it is a review request addressed to somebody who did
not ask for it. [CONTRIBUTING.md](CONTRIBUTING.md) has the rest.

## Status and roadmap

| | | |
|---|---|---|
| **Engine** — turn loop, ports, immutable tool sets, sub-agents, guards | ✅ | zero deps |
| **The gate** — per-action policy, durable grants, approver port | ✅ | 100 % covered |
| **WASM sandbox** — foreign code at zero authority, brokered imports | ✅ | |
| **Secret vault** — boundary injection, bidirectional leak scanning | ✅ | |
| **Out-of-band approvals** — WebSocket, APNs wake, first answer wins | ✅ | |
| **iOS companion app** — the second device | ✅ | TestFlight with the beta |
| **Remote MCP** — HTTPS servers, host-injected OAuth | ✅ | stdio deliberately not |
| **Plugins & skills** — sandboxed, manifest reviewed without running it | ✅ | |
| **Memory** — durable notes, catalog folded into every prompt | ✅ | |
| **Agents on a schedule** — cron, `strict` by default | ✅ | |
| **Knowledge** — document search, hybrid, reconciled every minute | 🆕 | newest, least worn in |
| **Voice & speaker recognition** — live audio, ESP32 satellite | 🚧 | expect it to move |
| **Hosted push relay** — so a phone can be woken without your own Apple account | 📋 | safe to hand off: a push carries no authority |
| **Android app** | 📋 | a build target, not a rewrite |
| **`agentkit` in its own repository** | 📋 | |
| **Ed25519 signing** for skills and plugins | 📋 | |
| **Local embeddings** — retrieval without a remote provider | 📋 | needs a transformer in `internal/onnx` |
| **Keychain vault backend**, interactive unlock | 📋 | replaces a passphrase in the environment |
| **`exec` tool** | ❌ | see [ADR-6](ADRS.md) — it erodes the one thing this design is for |

Two of those deserve a sentence rather than a row.

**Voice and speaker recognition** work end to end and are covered by tests — they are simply the
newest thing here, and the wire protocol is still moving. Recognition chooses **context and address,
never permission**: speech is a channel like the chat, where nobody authenticates the typist either.

**Knowledge** searches documents you file in a workspace, and indexing sends them to an embedding
provider. That is stated where you would look for it rather than in a footnote, because it is the
one part of this design that hands your data to somebody else on purpose.

## Reading further

- **[Documentation](https://efuturetoday.github.io/nocturn)** — guides, reference, architecture
- **[examples/](examples/)** — a workspace with one of everything: an agent, a skill, a plugin, an
  MCP server, memory, documents to search
- **[ADRS.md](ADRS.md)** · **[agentkit/DOCS.md](agentkit/DOCS.md)** · **[CLAUDE.md](CLAUDE.md)**
- **[CONTRIBUTING.md](CONTRIBUTING.md)** · **[SECURITY.md](SECURITY.md)**

## License

[Apache-2.0](LICENSE).
