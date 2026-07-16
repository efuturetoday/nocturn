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

### `file.read` <span class="axis axis--read">read</span>

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | yes      | Workspace-relative path to read. |

**Returns** the file contents (UTF-8), capped at 1 MiB.

### `file.list` <span class="axis axis--read">read</span>

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | no       | Workspace-relative directory. Defaults to the workspace root. |

**Returns** a JSON array: `[{ "name": "todo.md", "isDir": false, "size": 128 }]`.

### `file.stat` <span class="axis axis--read">read</span>

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | yes      | Workspace-relative path to stat. |

**Returns** `{ "exists": true, "isDir": false, "size": 128 }`. A missing path returns
`{ "exists": false }`.

### `file.search` <span class="axis axis--read">read</span>

Find files by glob pattern, walking subdirectories under a base directory.

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `pattern` | string | yes      | Glob. A pattern with **no `/`** (e.g. `*.md`) matches file **names** at any depth; a pattern with a `/` (e.g. `src/*.go`) matches the path relative to the search base. |
| `path`    | string | no       | Workspace-relative directory to search under. Defaults to the workspace root. |

**Returns** a JSON array of workspace-relative paths — e.g. `["a.md", "sub/b.md"]` — each usable
directly by the other `file.*` tools. Capped at 500 results; a truncated sweep appends a
`(truncated at 500 results)` line after the JSON so a capped result never reads as "found
everything".

### `file.write` <span class="axis axis--write">write</span>

Changes the world, so it **asks for approval** unless a standing grant covers it.

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `path`    | string | yes      | Workspace-relative path to write. Parent directories are created. |
| `content` | string | yes      | File contents (UTF-8). |

**Returns** `{ "path": "notes/todo.md", "bytesWritten": 8 }`.

### `file.remove` <span class="axis axis--write">write</span>

Changes the world, so it **asks for approval** unless a standing grant covers it.

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | yes      | Workspace-relative path to remove (a file or an empty directory). |

**Returns** `{ "path": "gone.txt", "removed": true }`.

### `file.move` <span class="axis axis--write">write</span>

Move or rename a file within the workspace. **Both** endpoints are confined to the workspace
(see [Confinement](#confinement)); parent directories of the destination are created. It is
gated on the **destination** path — that is the target a grant or cage scopes.

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `from` | string | yes      | Workspace-relative source path. |
| `to`   | string | yes      | Workspace-relative destination path. |

**Returns** `{ "from": "a.txt", "to": "b.txt" }`.

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
