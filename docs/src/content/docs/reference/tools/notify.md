---
title: notify
description: Send a fire-and-forget notification to the user's device.
---

**Capability:** [`notify`](/reference/notify/) · <span class="axis axis--read">read</span>

Send a notification to your device. It does **not** ask a question or wait for a reply — for that,
an effect goes through an [approval](/guides/approvals/) instead. Why it runs silently and how it
stays safe (host-owned destination, leak-scan, rate limit) live on the
[`notify` capability](/reference/notify/).

## Input

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `message` | string | yes      | The notification text. |
| `title`   | string | no       | Optional short title. |

## Output

```json
{ "sent": true }
```

## From JavaScript

```js
// wrapper (idiomatic):
nocturn.notify("Backup finished", "Nocturn");

// or the generic gate:
nocturn.call("notify", { message: "Backup finished", title: "Nocturn" });
```
