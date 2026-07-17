---
title: Capabilities reference
description: Every host capability Nocturn exposes — its tools, inputs, read/write axis, target, and the data it returns.
---

A **capability** is a real-world power the host lends the assistant — reaching the network,
touching the workspace, messaging you. A **tool** is what the model actually calls (`http.write`,
`file.read`); each tool exercises exactly one capability, and *that* is the authority the broker
gates. Several tools can share one capability — `http.read` and `http.write` are both `http`.
Everything the model, a script, a plugin, or an MCP server can *actually do* passes through a
capability; a WASM guest has zero ambient authority, so a capability it was not handed is simply
absent.

Two lists follow: the **capabilities** — one per family, each with its own page showing how to
cage it and what credentials it handles — and every **tool**, linked back to the capability it
exercises.

## The two axes

Every capability call is described on **two independent axes**. Keeping them apart is what
lets the gateway be precise instead of blunt.

- **Reach** — *where* the call goes. This is the **family** (`http`, `dns`, `file`) plus a
  **target** (a hostname for `http`/`dns`, a workspace-relative path for `file`). A limit on
  reach says *"this agent may touch `*.github.com`, nothing else"*.
- **Effect** — *what* the call does to the world: **read** or **write**. This is derived from
  the real operation, never from the tool's name — an HTTP `GET` is a read, a `POST` is a
  write; a `file.read` is a read, a `file.write` is a write.

The default rule is simple: **reads happen, writes ask.** Because the two axes are separate,
you can say "reads on any host are fine, but a write always asks" without enumerating hosts,
and a grant for *writing* to one host never silently permits *reading* another. See
[how approval decides](/guides/approvals/#how-the-decision-is-made) for the full picture.

## What a target looks like (it is per family)

There is one `target` field, but its **shape is defined by the family** — it is whatever
resource string that capability reaches. So the same slot means a hostname for the network
families and a path for the file family:

| Family              | `target` is…                                   | Example                 |
|---------------------|------------------------------------------------|-------------------------|
| `http`, `dns`, `icmp` | a **hostname**, an **IP**, or a **CIDR range** | `gmail.googleapis.com`, `10.0.0.0/8`  |
| `file`              | a **workspace-relative path** (glob)           | `notes/*`           |

That single `(family, target, access)` triple is exactly how a limit is written down. In a
[plugin's cage](/guides/writing-plugins/#declare-it) (`plugin.json`) it looks like this:

```json
{ "family": "http", "target": "gmail.googleapis.com", "access": ["read", "write"] }
{ "family": "http", "target": "10.0.0.0/8",           "access": ["read"] }
```

- **`family` + `target`** = the *reach* axis (which capability, which host/path). A glob is
  allowed (`*.github.com`, `notes/*`); a path glob does not cross `/`, so it is depth-bounded.
  For the network families the target may also be an **IP** or a **CIDR range**
  (`10.0.0.0/8`, `192.168.0.0/16`, `2001:db8::/32`) — the reach limiter matches an IP target
  numerically, so you can confine a caller to a subnet. A CIDR bounds calls made **to an IP in
  that range**; a call made to a *hostname* is matched by a hostname glob, because the broker
  never resolves names (that would be a DNS effect inside the decision layer).
- **`access`** = the *effect* axis, spelled out as `["read"]`, `["write"]`, or `["read","write"]`.
  It must be explicit — a missing `access` is a fail-closed error, never a silent "both".

The same triple is the vocabulary everywhere a bound is set: a plugin's cage, an agent's reach,
and your remembered [grants](/guides/approvals/#how-the-decision-is-made). Only the target's
*shape* changes with the family; the structure never does.

## The capabilities

Six capability families exist today. Each has its own page — how to cage it, the credentials it
handles, and its tools:

| Capability | Reaches | Target | Effects | Cage by |
|------------|---------|--------|---------|---------|
| [`http`](/reference/http/) | the network | host / IP / CIDR | read · write | target |
| [`dns`](/reference/dns/) | name resolution | hostname | read | target |
| [`icmp`](/reference/icmp/) | reachability probes (tool `ping`) | host / IP | read | target |
| [`file`](/reference/files/) | the workspace filesystem | workspace path | read · write | target |
| [`notify`](/reference/notify/) | messaging you | your channel (host-owned) | read | family only |
| [`remind`](/reference/reminders/) | scheduled messages to you | your channel (host-owned) | read | family only |

**Cage by target** means you scope reach to specific hosts or paths (`http.write @ api.github.com`,
`file.write @ notes/*`). **Family only** means the destination is host-owned and fixed (your own
channel), so you allow or deny the whole capability rather than picking a target.

Each tool has its own page under **Tools** in the sidebar, grouped by the capability it exercises —
including the ungated tools (`code.run`, `skill.*`, `time.now`, `wake`) that reach no capability at
all.

## One door for everyone

The model, a `code.run` script, a plugin, and an MCP client are four different callers, and
all four reach these capabilities through the **same** gateway. There is no back channel.
That is the whole point: to reason about what the assistant can do, you only have to reason
about this catalogue and the one gate in front of it.
