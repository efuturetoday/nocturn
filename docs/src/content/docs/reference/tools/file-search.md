---
title: file.search
description: Find workspace files by glob pattern.
---

**Capability:** [`file`](/reference/files/) · <span class="axis axis--read">read</span>

Find files by glob pattern, walking subdirectories. Runs silently under the default policy. See the
[`file` capability](/reference/files/) for confinement and cage syntax.

## Input

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `pattern` | string | yes      | Glob. No `/` (e.g. `*.md`) matches file **names** at any depth; a pattern with a `/` (e.g. `src/*.go`) matches the path relative to the search base. |
| `path`    | string | no       | Workspace-relative directory to search under. Defaults to the workspace root. |

## Output

A JSON array of workspace-relative paths — e.g. `["a.md", "sub/b.md"]` — each usable directly by
the other `file.*` tools. Capped at 500 results; a truncated sweep appends a
`(truncated at 500 results)` line after the JSON.

## From JavaScript

```js
// wrapper (idiomatic):
const mds = await nocturn.fs.search("*.md");

// or the generic gate:
const paths = JSON.parse(nocturn.call("file.search", { pattern: "*.md" }));
```
