---
title: Reminders
description: remind — a persistent notification at a future time, captured now, fired with no model run.
---

The `remind` family schedules a **notification at a future time**. At that time the user is
notified with `message` — no model run, the text is fixed when you create it. It is the
persistent, decoupled cousin of [`wake`](/reference/tools/wake/) (which resumes the live session).

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

Three tools exercise this capability — each page has its inputs, output, and a JavaScript example:

- [`remind`](/reference/tools/remind/) <span class="axis axis--read">read</span> — schedule a reminder
- [`remind.list`](/reference/tools/remind-list/) <span class="axis axis--read">read</span> — list pending reminders
- [`remind.cancel`](/reference/tools/remind-cancel/) <span class="axis axis--read">read</span> — cancel a reminder

## Limiting reach

Like [`notify`](/reference/notify/#limiting-reach), the destination is the **host-owned channel**,
not a model-chosen target — so there is no per-target scoping and the only sensible target is `*`.
In a plugin cage you allow or deny the whole `remind` capability:

```json
{ "family": "remind", "target": "*", "access": ["read"] }
```

This is a **family-level** allow/deny rather than the host/path scoping the
[network](/reference/http/) and [file](/reference/files/) capabilities offer. To require approval
on every reminder instead of the silent default, a workspace or agent policy tightens `remind` to
**ask**.

## Why it runs silently — and why that is safe

A reminder is a scheduled [notification](/reference/notify/) to your own device, so — like
`notify` — it runs silently under the default policy (no per-reminder approval). What keeps it
safe is structural: the destination is host-owned, the message is **leak-scanned** on create and
again at fire, and the store lives in the control-plane where the model can't forge it. It still
passes the one gateway, so a policy can tighten `remind` to ask.

Delivery goes over the same out-of-band channel as approvals (a phone push, or a dim TUI line when
none is configured).
