---
title: file.remove
description: Remove a file (or empty directory) from the workspace.
---

**Capability:** [`file`](/reference/files/) · <span class="axis axis--write">write</span>

Remove a file or empty directory. **Asks for approval** unless a standing grant covers it. See the
[`file` capability](/reference/files/) for confinement and cage syntax.

## Input

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | yes      | Workspace-relative path to remove (a file or an empty directory). |

## Output

```json
{ "path": "gone.txt", "removed": true }
```

## From JavaScript

```js
// wrapper (idiomatic):
const fs = require("fs");
fs.unlinkSync("gone.txt");

// or the generic gate:
const res = JSON.parse(nocturn.call("file.remove", { path: "gone.txt" }));
```
