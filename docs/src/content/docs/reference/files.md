---
title: Files capability
description: file.read, file.write, file.list, file.stat, file.search, file.remove, file.move — the workspace filesystem, confined by construction.
---

The `file` family gives the assistant a filesystem — but only **inside the workspace**. It is
the proof that the broker's `(family, target)` model is not HTTP-shaped: here the target is a
**path**, glob-matched exactly like a hostname is.

## At a glance

|                 |                                                        |
|-----------------|--------------------------------------------------------|
| **Family**      | `file`                                                 |
| **Target**      | a **workspace-relative path** — e.g. `notes/todo.md`   |
| **Tools**       | `file.read`, `file.list`, `file.stat`, `file.search` <span class="axis axis--read">read</span> · `file.write`, `file.remove`, `file.move` <span class="axis axis--write">write</span> |
| **Default policy** | reads run silently; writes ask for approval         |

## Tools

Seven tools exercise this capability — each page has its inputs, output, and a JavaScript example:

- [`file.read`](/reference/tools/file-read/) <span class="axis axis--read">read</span> — read a text file
- [`file.list`](/reference/tools/file-list/) <span class="axis axis--read">read</span> — list a directory
- [`file.stat`](/reference/tools/file-stat/) <span class="axis axis--read">read</span> — stat a path
- [`file.search`](/reference/tools/file-search/) <span class="axis axis--read">read</span> — find files by glob
- [`file.write`](/reference/tools/file-write/) <span class="axis axis--write">write</span> — write a text file
- [`file.remove`](/reference/tools/file-remove/) <span class="axis axis--write">write</span> — remove a file
- [`file.move`](/reference/tools/file-move/) <span class="axis axis--write">write</span> — move / rename a file

Reads run silently under the default policy; writes ask for approval.

## Limiting reach — cage syntax

A cage bounds where this capability may reach. For `file` the `target` is a workspace-relative
path (a glob is allowed); `access` is the read/write axis. See the
[capabilities overview](/reference/capabilities/#what-a-target-looks-like-it-is-per-family)
for the shared `(family, target, access)` rules.

```json
{ "family": "file", "target": "notes/*", "access": ["read", "write"] }
{ "family": "file", "target": "inbox/*", "access": ["read"] }
{ "family": "file", "target": "*",       "access": ["read"] }
```

- A path glob (`notes/*`) does **not** cross `/`, so it is depth-bounded for free — it covers
  `notes/todo.md` but not `notes/2026/todo.md`. Use `*` on its own to mean **any path** in the
  workspace.
- `access` must be explicit — a missing `access` is a fail-closed error, never a silent "both".

## Confinement

Every path is resolved against the workspace root **before the gateway is even consulted**. A
path that would escape it — via `..` or an absolute path outside the root — is a hard error, not
a denied call. The cage scopes *within* the workspace; this guarantees there is no outside. So a
`file` capability can never touch a byte the workspace does not contain, no matter what path the
model asks for.
