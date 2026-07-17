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

One tool exercises this capability — its page has the inputs, output, and a JavaScript example:

- [`notify`](/reference/tools/notify/) <span class="axis axis--read">read</span> — send a fire-and-forget notification to your device

## Cage syntax

The destination is the **host-owned channel**, not a model-chosen target, so there is no
meaningful per-target scoping — the only sensible target is `*`. In a plugin cage you therefore
allow or deny `notify` as a whole:

```json
{ "family": "notify", "target": "*", "access": ["read"] }
```

Because the target is fixed, this is effectively a **family-level** allow/deny, rather than the
host/path scoping the [network](/reference/http/) and [file](/reference/files/) capabilities offer.
To go the other way — require approval on *every* notification instead of the silent default — a
workspace or agent policy tightens `notify` to **ask** (see the safety note below).

## Limits

- **Rate limit** — _TBD_. Enforced **per capability family, not per tool** once wired; an anti-spam
  rate limiter is planned for `notify` but is not yet attached to the Guard, so no cap is enforced
  today.

## Leak scanning

The message is **leak-scanned on egress** before it leaves: a stored vault secret in the `message`
or `title` is **blocked**, so a prompt-injected caller cannot smuggle a credential out inside a
notification. `notify` injects no credentials of its own — nothing flows *in*.

## Why it runs silently — and why that is safe

A notification to your **own** device is not the kind of world-changing effect that warrants a
per-message *"may I tell you this?"* prompt, so under the default policy it runs silently. What
keeps that safe is **structural**, not a prompt: the destination is **host-owned, never
model-chosen** — the message goes to *your* configured channel, and the model supplies only the
*content*, never the *target* (exactly like host-side credential injection). So `notify` can never
become an exfiltration channel to a third party.

It still passes the one gateway, so a workspace or agent **policy can tighten it to ask** if you
want every notification approved.

## Channel

Notifications are delivered over the same out-of-band channel as approvals (ntfy — see
[getting set up](/guides/getting-started/)). When no channel is configured, a notification falls
back to a dim inline line in the TUI (the attended case).
