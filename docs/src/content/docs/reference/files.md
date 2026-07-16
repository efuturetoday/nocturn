---
title: Files capability
description: file.read, file.write, file.list, file.stat, file.remove — the workspace filesystem, confined by construction.
---

The `file` family gives the assistant a filesystem — but only **inside the workspace**. It is
the proof that the broker's `(family, target)` model is not HTTP-shaped: here the target is a
**path**, glob-matched exactly like a hostname is.

- **Family:** `file`
- **Target:** a **workspace-relative path** (e.g. `notes/todo.md`). Cages and grants scope on
  it — `file.write @ notes/*` covers writing under `notes/`, and because a path glob does not
  cross `/`, that grant is depth-bounded for free.
- **Confinement is by construction.** Every path is resolved against the workspace root first.
  A path that would escape (via `..` or an absolute path outside the root) is a **hard error
  before the gateway is even consulted** — the cage scopes *within* the workspace; this
  guarantees there is no outside.

## Reads (run silently)

These are observations — read axis, so they run under the default policy without asking.

### `file.read`

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | yes      | Workspace-relative path to read. |

**Returns** the file contents (UTF-8), capped at 1 MiB.

### `file.list`

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | no       | Workspace-relative directory. Defaults to the workspace root. |

**Returns** a JSON array: `[{ "name": "todo.md", "isDir": false, "size": 128 }]`.

### `file.stat`

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | yes      | Workspace-relative path to stat. |

**Returns** `{ "exists": true, "isDir": false, "size": 128 }`. A missing path returns
`{ "exists": false }`.

## Writes (ask first)

These change the world — write axis, so they **ask for approval** unless a standing grant
covers them.

### `file.write`

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `path`    | string | yes      | Workspace-relative path to write. Parent directories are created. |
| `content` | string | yes      | File contents (UTF-8). |

**Returns** `wrote N bytes to <path>`.

### `file.remove`

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | yes      | Workspace-relative path to remove (a file or an empty directory). |

**Returns** `removed <path>`.
