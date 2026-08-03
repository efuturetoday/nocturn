---
title: The companion app
description: The second device — what it is for, how it pairs, and why an approval that arrives here is the one an attacker cannot answer.
---

The app is not a remote control for the terminal. It is the **second device**, and it is the reason
Nocturn's approvals mean anything at all.

An assistant that asks permission *inside the conversation* asks in the same place a
[prompt injection](/nocturn/architecture/threat-model/) already sits. The injection can answer it.
Move the question to a device it never reached, and it cannot. Everything else the app does —
reading chats, firing agents, checking reminders — is convenience layered on that one job.

## Getting it

The app is **iOS today**, built with Angular under Capacitor. A **TestFlight link lands here with
the public beta**; an Android build is planned, and the framework choice is what makes that a build
target rather than a rewrite — what genuinely differs is the push transport.

Until then you can build it yourself from `mobile/` in the repository; the README there has the
steps.

## Pairing

The daemon has to be running: `nocturn serve`. The app finds it on your network over Bonjour, so
you do not type an IP.

![The app's connect screen: a daemon found on the network by name, its WebSocket address underneath, and a link to enter a server by hand.](../../../assets/screenshots/app-connect-discovery.jpg)

- **The first device** redeems a bootstrap code the daemon prints at startup. That is the only time
  a code comes from the machine itself.
- **Every device after that** asks an already-paired one. The new device requests a join, the
  request appears live on your existing devices, and the code is shown *there* — you read it on the
  trusted device and enter it on the new one.

![The pairing sheet: six empty code boxes, the daemon's address above them, and a link for a device that already has a paired sibling to join instead.](../../../assets/screenshots/app-pair-device.jpg)

A device merely on your network cannot join. It needs a human at a device you already trust.

The full flow, including devices with no screen to show a code, is in
[remote access](/nocturn/guides/remote-access/).

## What it does

| | |
|---|---|
| **Approvals** | the point — an ask, what it would reach, approve or deny |
| **Chats** | every conversation, streamed token by token, shared with the terminal |
| **Agents** | the scheduled agents in a workspace, and firing one by hand |
| **Reminders** | what the assistant has been asked to bring back to you |
| **Pairing and joins** | approving the next device |
| **Discovery** | finding daemons on the network |

![The Home tab: a pending reminder with the time it fires, and the workspace's recent conversations underneath.](../../../assets/screenshots/app-home-reminders-recent-chats.jpg)

![The Chat tab: every conversation in the workspace, newest last, each with the assistant's last line.](../../../assets/screenshots/app-chat-list.jpg)

Conversations are **not per device**. Start something in the terminal, pick it up on the phone,
finish it in the terminal again. And when two devices are shown the same approval, **the first
answer wins** — the other is told it was resolved rather than asked twice.

## Seeing what a turn actually did

Every assistant turn that used tools carries one chip above the answer, naming them. Nothing runs
invisibly here either — the chip is there whether or not the answer mentions it.

![A chat turn: the chip above the answer reads `remind`, and the reply confirms the reminder was set for five minutes from now.](../../../assets/screenshots/app-chat-remind.jpg)

Tapping the chip opens the turn's tools, each expanding to the arguments it was called with and the
result it returned:

![The tools window for that turn: `remind` succeeded in 10 ms, with the input arguments and the returned reminder id and fire time.](../../../assets/screenshots/app-tool-detail-remind.jpg)

## Approvals when you are nowhere near it

This is the case the daemon exists for. An agent runs on a schedule, hits something that needs your
yes, and there is no terminal in front of it. A push wakes your phone, you decide, the run continues
or stops.

![An approval sheet over the chat: the kind and target read `net → google.com`, with Once, Session, Always and Deny.](../../../assets/screenshots/app-approval-net-google.jpg)

**The push carries no authority.** It is a wake signal and nothing else — the decision travels back
over the authenticated WebSocket, never inside the notification. A notification that arrives twice,
or is replayed, cannot approve anything. That property is also what will make a
[hosted relay](/nocturn/guides/remote-access/) safe to run later: a relay learns that some device
should look at its daemon, and nothing about what was asked or how you answered.

Push over the internet currently needs your own Apple Developer account and the four
`NOCTURN_APNS_*` variables. Without them everything still works on your own network — you simply are
not woken while the app is closed.

With no paired device at all, an agent set to `guarded` behaves as `strict`: the ask is denied and
the run reports why. Missing setup reduces authority; it never grants it.

## What it deliberately cannot do

The app is a device with a **class**, and the class decides what it may do — not a list of
permissions stored somewhere. An `app` may answer approvals and enrol other devices. An `appliance`
— a voice satellite, anything with no authenticated input — may do neither, and is never even
attached to the approval broker, so there is nothing for it to answer *with*.

A device may only enrol a class whose abilities its own class already covers. A stolen speaker
therefore cannot multiply into a household of equally trusted microphones.
