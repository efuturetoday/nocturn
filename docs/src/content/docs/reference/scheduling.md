---
title: Scheduling
description: remind — a persistent notification at a future time; wake — the agent resuming its own session after a delay.
---

Two different time mechanisms, deliberately kept apart:

|                | `remind`                                    | `wake`                                       |
|----------------|---------------------------------------------|----------------------------------------------|
| **Fires**      | a plain notification (no model run)         | **this same session**, resumed with a note   |
| **Context**    | decoupled — the text is fixed now           | preserved — the conversation continues       |
| **Lifetime**   | **persistent** (survives restart)           | **ephemeral** (only while the process runs)  |
| **Authority**  | gated (silent), leak-scanned                | **none external** — bounded, not gated       |
| **For**        | "remind me tomorrow about X"                | self-paced loops / polling ("re-check in 5m")|

## `remind` — a notification at a future time

At the given time the user is notified with `message` — no model run, the text is fixed when
you create it. Reminders are **persistent**: they are stored in the workspace control-plane
(`reminders.json`, outside the model's mount, so the model can neither read nor `file.write` it —
the only way to add or cancel one is this gated tool) and re-enrolled on the next start.

### `remind` <span class="axis axis--read">read</span>

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `when`    | string | yes      | An absolute **RFC3339** timestamp, or `"in <duration>"` (e.g. `"in 2h"`, `"in 90m"`). For a wall-clock time like "tomorrow 9am", compute it with `time.now` and pass the RFC3339 result. |
| `message` | string | yes      | What to remind the user about. Leak-scanned — a vault secret is blocked. |
| `title`   | string | no       | Optional short title. |

**Returns** `{ "id": "rem-…", "fireAt": "2026-07-18T09:00:00+02:00" }`.

- **`remind.list`** — returns the pending reminders as JSON.
- **`remind.cancel {id}`** — cancels one; returns `{ "id", "cancelled" }`.

Reminders run **silently** (a benign future notification needs no per-reminder approval), but
still pass the one gateway, so a policy can tighten `remind` to ask. Delivery goes over the same
channel as [approvals](/reference/notify/) (a phone push, or a TUI line when none is configured).

## `wake` — resume yourself later

`wake` schedules the running agent's **own re-invocation**: after `seconds`, this same
conversation is re-invoked with `note` as the prompt. Use it to wait and then continue — poll
something, or re-check after a delay. It resumes the **same session** (context preserved), and
only ever fires *after* the current turn has ended (it is not re-entrant).

### `wake`

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `seconds` | number | yes      | Delay before resuming, **clamped to 60…3600**. |
| `note`    | string | yes      | The prompt to resume with, e.g. `"re-check the deploy status"`. |

**Returns** `{ "wakeInSeconds": 300 }`.

`wake` reaches **nothing external** — it only schedules a continuation — so it carries no
authority and is **not gated** (like `time.now`). The risk is a runaway self-waking loop, so it
is **bounded** instead: the delay is clamped and the number of pending wakes is capped. Any real
effect performed in the *resumed* turn still passes the broker + approvals normally.

It is **ephemeral**: pending wakes live only as long as the process, and a new session
(Ctrl+N) drops them — a dead conversation is never resumed.
