---
title: remind.cancel
description: Cancel a pending reminder by id.
---

**Capability:** [`remind`](/reference/reminders/) · <span class="axis axis--read">read</span>

Cancel a pending reminder. See the [`remind` capability](/reference/reminders/) for details.

## Input

| Field | Type   | Required | Notes |
|-------|--------|----------|-------|
| `id`  | string | yes      | The reminder id (from [`remind`](/reference/tools/remind/) or [`remind.list`](/reference/tools/remind-list/)). |

## Output

```json
{ "id": "rem-…", "cancelled": true }
```

## From JavaScript

```js
// no wrapper — use the generic gate:
const res = JSON.parse(nocturn.call("remind.cancel", { id: "rem-abc" }));
```
