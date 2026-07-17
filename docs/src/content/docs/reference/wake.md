---
title: Wake
description: wake — the agent resuming its own session after a delay, for self-paced loops and polling.
---

`wake` schedules the running agent's **own re-invocation**: after `seconds`, this same
conversation is re-invoked with `note` as the prompt. Use it to wait then continue — poll
something, or re-check after a delay ("check the deploy again in 5 minutes").

It is the ephemeral, in-session cousin of a [reminder](/reference/reminders/): a reminder fires a
detached notification later; `wake` resumes **this session**, with its context preserved. It only
ever fires *after* the current turn has ended — it is not re-entrant.

## `wake`

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `seconds` | number | yes      | Delay before resuming, **clamped to 60…3600**. |
| `note`    | string | yes      | The prompt to resume with, e.g. `"re-check the deploy status"`. |

**Returns** `{ "wakeInSeconds": 300 }`.

## Not a capability — bounded, not gated

`wake` reaches **nothing external** — it only schedules a continuation — so it carries no
authority and is **not gated** (like `time.now`; it is not in the [capability catalogue](/reference/capabilities/)).
Any real effect performed in the *resumed* turn still passes the broker + approvals normally.

The risk `wake` does carry is a runaway self-waking loop, so it is **bounded** instead of gated:

- the delay is **clamped** to 60…3600 seconds — a wake can neither hammer nor pin resources forever;
- the number of **pending wakes is capped**, so an injected caller can't schedule an unbounded fan-out.

It is **ephemeral**: pending wakes live only as long as the process, and a new session (Ctrl+N)
drops them — a dead conversation is never resumed.
