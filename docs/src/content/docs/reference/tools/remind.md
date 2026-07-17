---
title: remind
description: Schedule a persistent notification at a future time.
---

**Capability:** [`remind`](/reference/reminders/) · <span class="axis axis--read">read</span>

Schedule a notification for a future time. At that time you are notified with `message` — no model
run, the text is fixed when you create it. Persistence and safety live on the
[`remind` capability](/reference/reminders/).

## Input

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `when`    | string | yes      | An absolute **RFC3339** timestamp, or `"in <duration>"` (e.g. `"in 2h"`). For a wall-clock time, compute it with [`time.now`](/reference/tools/time-now/) and pass the RFC3339 result. |
| `message` | string | yes      | What to remind you about. Leak-scanned — a vault secret is blocked. |
| `title`   | string | no       | Optional short title. |

## Output

```json
{ "id": "rem-…", "fireAt": "2026-07-18T09:00:00+02:00" }
```

## From JavaScript

```js
// wrapper (idiomatic):
nocturn.remind("in 2h", "stretch");

// or the generic gate:
nocturn.call("remind", { when: "in 2h", message: "stretch" });
```
