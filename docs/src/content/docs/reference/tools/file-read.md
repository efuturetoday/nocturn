---
title: file.read
description: Read a UTF-8 text file from the workspace.
---

**Capability:** [`file`](/reference/files/) · <span class="axis axis--read">read</span>

Read a UTF-8 text file. Runs silently under the default policy. Workspace confinement and cage
syntax live on the [`file` capability](/reference/files/).

## Input

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | yes      | Workspace-relative path to read. |

## Output

The file contents (UTF-8), capped at 1 MiB.

## From JavaScript

```js
// wrapper (idiomatic):
const fs = require("fs");
const text = fs.readFileSync("notes/todo.md");

// or the generic gate:
const text2 = nocturn.call("file.read", { path: "notes/todo.md" });
```
