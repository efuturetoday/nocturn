---
title: http.read
description: Read a URL over HTTP(S) with a safe method (GET/HEAD).
---

**Capability:** [`http`](/reference/http/) · <span class="axis axis--read">read</span>

Read a URL with a safe method. Runs silently under the default policy. All the reach, cage, and
credential behaviour lives on the [`http` capability](/reference/http/) page.

## Input

| Field    | Type   | Required | Notes |
|----------|--------|----------|-------|
| `url`    | string | yes      | The URL to read. |
| `method` | string | no       | `GET` or `HEAD`. Default `GET`. A mutating method is rejected. |

## Output

JSON envelope — the real outcome, not just the body:

```json
{ "status": 200, "statusText": "OK", "headers": { "Content-Type": "application/json" }, "body": "…" }
```

## From JavaScript

```js
// wrapper (idiomatic):
const r = await fetch("https://api.example.com/items");
const items = await r.json();

// or the generic gate (works for every tool):
const res = JSON.parse(nocturn.call("http.read", { url: "https://api.example.com/items" }));
```
