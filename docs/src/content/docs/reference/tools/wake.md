---
title: wake
description: Pause and resume yourself later — schedule the agent's own re-invocation after a delay.
---

**Capability:** — (ungated)

Schedule the running agent's **own re-invocation**: after `seconds`, this same conversation is
re-invoked with `note` as the prompt. Use it to wait then continue — poll something, or re-check
after a delay ("check the deploy again in 5 minutes"). It is the ephemeral, in-session cousin of a
[reminder](/reference/reminders/) (which fires a detached notification instead).

## Input

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `seconds` | number | yes      | Delay before resuming. **Clamped** to the allowed range (default **1 s … 1 h**). |
| `note`    | string | yes      | The prompt to resume with, e.g. `"re-check the deploy status"`. |

## Output

```json
{ "wakeInSeconds": 300 }
```

## From JavaScript

```js
// wrapper (idiomatic):
nocturn.wake(300, "re-check the deploy status");

// or the generic gate:
nocturn.call("wake", { seconds: 300, note: "re-check the deploy status" });
```

## Bounded, not gated

`wake` reaches **nothing external** — it only schedules a continuation — so it carries no authority
and is not gated (like [`time.now`](/reference/tools/time-now/)). Any real effect performed in the
*resumed* turn still passes the broker + approvals normally. The one risk is a runaway self-waking
loop, so it is **bounded** instead:

- the delay is **clamped** (default 1 s … 1 h) — a wake can neither hammer nor pin resources forever;
- the number of **pending wakes is capped**, so an injected caller can't schedule an unbounded fan-out;
- it is **ephemeral** — pending wakes live only as long as the process, and a new session (Ctrl+N)
  drops them, so a dead conversation is never resumed. It only ever fires *after* the current turn
  ends (not re-entrant).
