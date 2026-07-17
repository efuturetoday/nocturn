---
title: Notify capability
description: notify — the assistant reaching you proactively (fire-and-forget), the other half of human-in-the-loop.
---

The `notify` family lets the assistant **reach you** — a message to your own device, sent and
forgotten. It is the other half of [approvals](/guides/approvals/): approval **asks and waits**;
notify just **tells** you something ("flight delayed", "the scheduled report is ready").

## At a glance

|                 |                                                        |
|-----------------|--------------------------------------------------------|
| **Family**      | `notify`                                               |
| **Target**      | the **user's channel** (host-owned, not model-chosen)  |
| **Tools**       | `notify` <span class="axis axis--read">read</span>     |
| **Default policy** | runs silently — a message to your own device is not per-message approved |

## Tools

### `notify` <span class="axis axis--read">read</span>

Send a notification to your device. It does **not** ask a question or wait for a reply — for that,
an effect goes through an approval instead.

| Field     | Type   | Required | Notes |
|-----------|--------|----------|-------|
| `message` | string | yes      | The notification text. |
| `title`   | string | no       | Optional short title. |

**Returns** `{ "sent": true }`.

## Why it runs silently — and why that is safe

A notification to your **own** device is not the kind of world-changing effect that warrants a
per-message *"may I tell you this?"* prompt, so under the default policy it runs silently. What
keeps that safe is **structural**, not a prompt:

- **The destination is host-owned, never model-chosen.** The message goes to *your* configured
  channel. The model supplies only the *content*, never the *target* — exactly like host-side
  [credential injection](/reference/http/#credentials--leak-scanning). So `notify` can never become
  an exfiltration channel to a third party.
- **The message is leak-scanned on egress.** A stored vault secret in the text is **blocked** — a
  prompt-injected caller cannot smuggle a credential out inside a notification.
- **It is rate-limited.** A runaway or injected caller cannot spam your device.

It still passes the one gateway, so a workspace or agent **policy can tighten it to ask** if you
want every notification approved.

## Channel

Notifications are delivered over the same out-of-band channel as approvals (ntfy — see
[getting set up](/guides/getting-started/)). When no channel is configured, a notification falls
back to a dim inline line in the TUI (the attended case).

## From a script

```js
nocturn.notify("Backup finished", "Nocturn");   // message, optional title
```
