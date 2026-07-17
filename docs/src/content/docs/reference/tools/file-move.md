---
title: file.move
description: Move or rename a file within the workspace.
---

**Capability:** [`file`](/reference/files/) · <span class="axis axis--write">write</span>

Move or rename a file within the workspace. **Asks for approval** unless a standing grant covers
it. **Both** endpoints are confined to the workspace; it is gated on the **destination** path. See
the [`file` capability](/reference/files/) for confinement and cage syntax.

## Input

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `from` | string | yes      | Workspace-relative source path. |
| `to`   | string | yes      | Workspace-relative destination path. Parent directories are created. |

## Output

```json
{ "from": "a.txt", "to": "b.txt" }
```

## From JavaScript

```js
// wrapper (idiomatic):
const fs = require("fs");
fs.renameSync("a.txt", "b.txt");

// or the generic gate:
const res = JSON.parse(nocturn.call("file.move", { from: "a.txt", to: "b.txt" }));
```
