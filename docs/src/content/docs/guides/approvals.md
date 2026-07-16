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

You do not have to be at the keyboard. Nocturn can send an approval request to your phone
as a push notification with **Approve** and **Deny** buttons. Tap one and the assistant
continues. While it waits, it simply pauses, so a slow answer never causes a timeout.

This is the recommended way to run it. Approving on a separate device means that even if
something hijacks the chat, it cannot approve its own actions. The decision lives somewhere
it cannot reach.

### Setting it up

Nocturn uses the free [ntfy](https://ntfy.sh) app for this.

1. Install **ntfy** on your phone, on iOS or Android.
2. In the app, subscribe to two topic names of your choosing, one for requests and one for
   replies. Pick names that are long and hard to guess.
3. Add them to your `.env` next to Nocturn:

   ```ini
   NTFY_REQ_TOPIC=your-requests-topic-name
   NTFY_RESP_TOPIC=your-replies-topic-name
   ```

4. Restart Nocturn. It prints a line confirming phone approvals are on.

That is it. From now on, when Nocturn needs a yes and you are not at the terminal, your
phone buzzes.

:::note[Self-hosting ntfy]
If you run your own ntfy server, point Nocturn at it with `NTFY_BASE_URL`, and add
`NTFY_TOKEN` if your server requires a login. The public server works fine to start.
:::

:::tip[Is it safe over a public service?]
Yes. The security does not depend on the topic names being secret. Every reply carries a
signed, single-use code that cannot be forged or reused, so a stranger who guessed your
topic still could not approve anything.
:::
