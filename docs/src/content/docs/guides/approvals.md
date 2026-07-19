---
title: Approving actions
description: What Nocturn asks about, how you answer, and how to approve from your phone.
---

Nocturn does the harmless things on its own and asks you about the rest. This page shows
what that feels like and how to move the asking to your phone.

## What it asks about

The rule of thumb:

- **Looking things up is free.** Reading a page or a file just happens.
- **Anything that changes the world asks first.** Sending a message, writing or deleting a
  file, or reaching a website it has not been cleared for.

So a normal research task flows without interruptions, and the assistant only stops you
when it is about to *act*.

## How the decision is made

Behind that rule of thumb, every action the assistant proposes is judged on **two separate
questions**. Keeping them apart is what lets the assistant be helpful without being reckless.

1. **Where does it reach?** Every action goes through a **capability** — a specific power like
   *reach the network* (`http`), *look up a name* (`dns`), or *touch a workspace file* (`file`)
   — aimed at a **target**: a website, or a file path. The assistant has no other powers; if a
   capability was not handed to it, it simply cannot do that thing. (See the
   [capabilities reference](/reference/capabilities/) for the full list.)
2. **What does it do — read or write?** This is the second axis. **Reading** looks something up
   and changes nothing. **Writing** changes the world: sending, saving, deleting. This is worked
   out from the real action, not from what it is called, so nothing can dress up a write as a
   read.

The **policy** is the standing rule over those two axes. Out of the box it is one line: *reads
happen, writes ask.* That is why looking things up never interrupts you, while anything that
acts stops for a yes.

Two things refine it:

- **Limits (cages).** A limit on *reach* — say, "this agent may only touch `*.github.com`".
  Anything outside is denied flat, without even asking, so a hijacked chat cannot talk you into
  approving something it was never allowed to attempt in the first place.
- **Your standing answers (grants).** When you say "allow this session" or "allow always", that
  answer is remembered — but **narrowly**, tied to the exact tool and target. Allowing *sending*
  email never quietly allows *deleting* it, and allowing writes to one site never covers another.

The order is always the same: outside the limits → denied; a read, or something you already
allowed → runs; otherwise → asks you. You can follow one real action all the way through on the
[request flow](/architecture/request-flow/) page.

## Answering a prompt

When Nocturn asks, you see a short description of what it wants to do, for example *Send
email to a@example.com*, with the plain technical detail underneath so there is no guessing.
You have four choices:

- **Allow once.** Just this one time.
- **Allow this session.** Until you start a new session or restart.
- **Allow always.** Remembered for next time too.
- **Deny.** Do not do it.

"Allow always" is narrow on purpose. Saying yes to *sending* email never quietly allows
*deleting* it. Some especially sensitive actions can only be allowed once. They are never
remembered, so you are asked every time.

If you ignore a prompt, nothing happens. Silence is always a no.

## Approve from your phone

You do not have to be at the keyboard. Nocturn reaches the **companion app** on your phone
with a push notification when it needs a yes; you open it and tap **Approve** or **Deny**,
and the assistant continues. While it waits, it simply pauses, so a slow answer never causes
a timeout.

This is the recommended way to run it. Approving on a separate device means that even if
something hijacks the chat, it cannot approve its own actions. The decision lives somewhere
it cannot reach.

### Setting it up

The companion app pairs to your daemon, and Nocturn pushes to it over Apple Push (APNs).

1. Start the daemon with `nocturn serve`. On first run — when no device is paired yet — it
   prints a **pairing QR** and a short code to the terminal.
2. Open the companion app and scan the QR (or type the code) to pair. The app stores a
   per-device key; you can revoke it later from any paired device. Pair a second device from
   the first: it shows a code the new device types in.
3. Give Nocturn Apple Push credentials so it can reach the phone, in your `.env`:

   ```ini
   NOCTURN_APNS_KEY=/path/to/AuthKey_XXXXXXXXXX.p8
   NOCTURN_APNS_KEY_ID=XXXXXXXXXX
   NOCTURN_APNS_TEAM_ID=YYYYYYYYYY
   NOCTURN_APNS_BUNDLE_ID=me.itexpert.nocturn
   ```

4. Restart Nocturn. From now on, when it needs a yes and no device is watching in the
   foreground, your phone gets a push.

:::note[On your network vs away]
On the same network as the daemon, answering is instant. Answering while away from home
needs a relay, which is on the roadmap; until then, an approval is answered once you are
reachable on the LAN (otherwise it times out — and a timeout is a no).
:::

:::tip[Is the push safe?]
Yes. The push carries no decision and no secret — it is only a nudge. The approve/deny is
sent back over the app's own bearer-authenticated connection, so even someone who intercepted
the push notification could not approve anything.
:::
