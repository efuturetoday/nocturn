---
title: file.write
description: Write a UTF-8 text file to the workspace.
---

**Capability:** [`file`](/reference/files/) · <span class="axis axis--write">write</span>

Write a UTF-8 text file (creating parent directories). **Asks for approval** unless a standing
grant covers it. Confinement and cage syntax live on the [`file` capability](/reference/files/).

## Input

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `path`    | string | yes      | Workspace-relative path to write. Parent directories are created. |
| `content` | string | yes      | File contents (UTF-8). |

## Output

```json
{ "path": "notes/todo.md", "bytesWritten": 8 }
```

## From JavaScript

```js
// wrapper (idiomatic):
const fs = require("fs");
fs.writeFileSync("notes/todo.md", "buy milk");

// or the generic gate:
const res = JSON.parse(nocturn.call("file.write", { path: "notes/todo.md", content: "buy milk" }));
```
