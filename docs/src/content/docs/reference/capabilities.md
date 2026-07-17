---
title: Capabilities reference
description: Every host capability Nocturn exposes — its tools, inputs, read/write axis, target, and the data it returns.
---

A **capability** is a real-world power the host lends the assistant: reaching the network,
touching the workspace, resolving a name. Everything the model, a script, a plugin, or an
MCP server can *actually do* passes through one of them. There is no other way out — a WASM
guest has zero ambient authority, so a capability it was not handed is simply absent.

This section is the catalogue. Each family (HTTP, DNS, Ping, Files, Notify) has its own page listing
every tool, its inputs, and what it returns. This page is the map over all of them.

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
| `http`, `dns`, `ping` | a **hostname**, an **IP**, or a **CIDR range** | `gmail.googleapis.com`, `10.0.0.0/8`  |
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

## Every tool at a glance

| Tool          | Family | Axis | Target              | Inputs                                   | Returns |
|---------------|--------|------|---------------------|------------------------------------------|---------|
| `http.read`   | `http` | <span class="axis axis--read">read</span> | hostname | `url`, `method` (`GET`/`HEAD`) | JSON `{status, statusText, headers, body}` |
| `http.write`  | `http` | <span class="axis axis--write">write</span> | hostname | `url`, `method` (`POST`/`PUT`/`PATCH`/`DELETE`), `body`, `content_type` | JSON `{status, statusText, headers, body}` |
| `dns.resolve` | `dns`  | <span class="axis axis--read">read</span> | hostname | `host`, `type` (`A`/`AAAA`/`IP`/`MX`/`TXT`/`CNAME`/`NS`/`PTR`/`SRV`) | JSON `{host, type, records}` |
| `ping`        | `ping` | <span class="axis axis--read">read</span> | hostname / IP | `host` | JSON `{host, ip, ok, rtt_ms}` |
| `file.read`   | `file` | <span class="axis axis--read">read</span> | path (in workspace) | `path` | file contents (UTF-8, ≤ 1 MiB) |
| `file.write`  | `file` | <span class="axis axis--write">write</span> | path (in workspace) | `path`, `content` | JSON `{path, bytesWritten}` |
| `file.list`   | `file` | <span class="axis axis--read">read</span> | path (in workspace) | `path` (directory) | JSON array `[{name, isDir, size}]` |
| `file.stat`   | `file` | <span class="axis axis--read">read</span> | path (in workspace) | `path` | JSON `{exists, isDir, size}` |
| `file.search` | `file` | <span class="axis axis--read">read</span> | path (in workspace) | `pattern`, `path` (base dir) | JSON array of matching paths |
| `file.remove` | `file` | <span class="axis axis--write">write</span> | path (in workspace) | `path` | JSON `{path, removed}` |
| `file.move`   | `file` | <span class="axis axis--write">write</span> | path (in workspace) | `from`, `to` | JSON `{from, to}` |
| `notify`      | `notify` | <span class="axis axis--read">read</span> | user's channel (host-owned) | `message`, `title` | JSON `{sent}` |
| `remind`      | `remind` | <span class="axis axis--read">read</span> | user's channel (host-owned) | `when`, `message`, `title` | JSON `{id, fireAt}` |
| `remind.list` | `remind` | <span class="axis axis--read">read</span> | user's channel (host-owned) | — | JSON array of reminders |
| `remind.cancel` | `remind` | <span class="axis axis--read">read</span> | user's channel (host-owned) | `id` | JSON `{id, cancelled}` |

The tool name **is** the authority. `http.read` and `http.write` are split so the tool the
model picks already decides the effect axis — the security layer never has to trust an HTTP
verb it was handed.

## Tools that are not capabilities

Some tools the model can call reach **no** capability, so they are never gated:

- **`code.run`** — runs JavaScript on the sandboxed interpreter. Pure computation needs zero
  authority. When a script wants an effect it calls `nocturn.call(tool, args)`, which routes
  back through *these same capabilities* and their gating — see the
  [WASM data format](/reference/wasm-abi/).
- **`skill.load` / `skill.read`** — pull in context (instructions, references). Context is
  not authority: a skill can only *suggest* how to use gated tools, never grant new reach.
- **`time.now`** — returns the current date and time (`{unix, iso, utc, timezone, offset_seconds}`).
  A clock read leaks nothing and changes nothing, so it carries zero authority. It exists as a host
  tool only because the sandbox guest has no wall clock of its own — without it a skill could not
  answer *"what is due today?"*.
- **`wake`** — schedules the agent's own resume after a delay (see [scheduling](/reference/scheduling/)).
  It reaches nothing external, so it is ungated — **bounded** instead (delay clamp + pending cap). Any
  effect in the *resumed* turn is gated normally.

## MCP tools

A remote [MCP server](/guides/remote-mcp/) contributes its own tools, but under the hood each
one is an `http` capability call to the MCP host. It rides the exact same reach/effect axes
(the MCP host is the target; a read-only tool is a read, otherwise a write), so remote tools
are gated and approved just like a native `http.write` — no separate machinery.

## One door for everyone

The model, a `code.run` script, a plugin, and an MCP client are four different callers, and
all four reach these capabilities through the **same** gateway. There is no back channel.
That is the whole point: to reason about what the assistant can do, you only have to reason
about this catalogue and the one gate in front of it.
