---
title: file.list
description: List the entries of a workspace directory.
---

**Capability:** [`file`](/reference/files/) · <span class="axis axis--read">read</span>

List a workspace directory. Runs silently under the default policy. See the
[`file` capability](/reference/files/) for confinement and cage syntax.

## Input

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `path` | string | no       | Workspace-relative directory. Defaults to the workspace root. |

## Output

```json
[{ "name": "todo.md", "isDir": false, "size": 128 }]
```

## From JavaScript

```js
// wrapper (idiomatic):
const fs = require("fs");
const names = fs.readdirSync("notes");

// or the generic gate:
const entries = JSON.parse(nocturn.call("file.list", { path: "notes" }));
```
