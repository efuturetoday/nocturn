---
title: file.stat
description: Stat a workspace path.
---

**Capability:** [`file`](/reference/files/) · <span class="axis axis--read">read</span>

Stat a workspace path. Runs silently under the default policy. See the
[`file` capability](/reference/files/) for confinement and cage syntax.

## Input

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | yes      | Workspace-relative path to stat. |

## Output

```json
{ "exists": true, "isDir": false, "size": 128 }
```

A missing path returns `{ "exists": false }`.

## From JavaScript

```js
// wrapper (idiomatic):
const fs = require("fs");
const exists = fs.existsSync("notes/todo.md");

// or the generic gate:
const s = JSON.parse(nocturn.call("file.stat", { path: "notes/todo.md" }));
```
