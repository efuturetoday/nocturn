---
title: remind.list
description: List the pending reminders.
---

**Capability:** [`remind`](/reference/reminders/) · <span class="axis axis--read">read</span>

List the pending reminders. See the [`remind` capability](/reference/reminders/) for how reminders
are stored and kept safe.

## Input

None.

## Output

A JSON array of `{ id, fireAt, message, title }`.

## From JavaScript

```js
// no wrapper — use the generic gate:
const reminders = JSON.parse(nocturn.call("remind.list", {}));
```
