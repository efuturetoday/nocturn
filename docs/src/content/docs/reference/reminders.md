---
title: Reminders
description: remind — a persistent notification at a future time, captured now, fired with no model run.
---

The `remind` family schedules a **notification at a future time**. At that time the user is
notified with `message` — no model run, the text is fixed when you create it. It is the
persistent, decoupled cousin of [`wake`](/reference/wake/) (which resumes the live session).

Reminders are **persistent**: they are stored in the workspace control-plane (`reminders.json`,
outside the model's mount, so the model can neither read nor `file.write` it — the only way to
add or cancel one is this gated tool) and re-enrolled on the next start.

## At a glance

|                 |                                                        |
|-----------------|--------------------------------------------------------|
| **Family**      | `remind`                                               |
| **Target**      | the **user's channel** (host-owned, not model-chosen)  |
| **Tools**       | `remind`, `remind.list`, `remind.cancel` <span class="axis axis--read">read</span> |
| **Default policy** | runs silently (a benign future notice), leak-scanned |

## Tools

### `remind` <span class="axis axis--read">read</span>

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `when`    | string | yes      | An absolute **RFC3339** timestamp, or `"in <duration>"` (e.g. `"in 2h"`, `"in 90m"`). For a wall-clock time like "tomorrow 9am", compute it with `time.now` and pass the RFC3339 result. |
| `message` | string | yes      | What to remind the user about. Leak-scanned — a vault secret is blocked. |
| `title`   | string | no       | Optional short title. |

**Returns** `{ "id": "rem-…", "fireAt": "2026-07-18T09:00:00+02:00" }`.

### `remind.list` <span class="axis axis--read">read</span>

Returns the pending reminders as a JSON array of `{id, fireAt, message, title}`.

### `remind.cancel` <span class="axis axis--read">read</span>

| Field | Type   | Required | Notes |
|-------|--------|----------|-------|
| `id`  | string | yes      | The reminder id (from `remind` or `remind.list`). |

**Returns** `{ "id": "rem-…", "cancelled": true }`.

## Why it runs silently — and why that is safe

A reminder is a scheduled [notification](/reference/notify/) to your own device, so — like
`notify` — it runs silently under the default policy (no per-reminder approval). What keeps it
safe is structural: the destination is host-owned, the message is **leak-scanned** on create and
again at fire, and the store lives in the control-plane where the model can't forge it. It still
passes the one gateway, so a policy can tighten `remind` to ask.

Delivery goes over the same out-of-band channel as approvals (a phone push, or a dim TUI line when
none is configured).
