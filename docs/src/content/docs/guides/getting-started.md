---
title: Getting started
description: Download Nocturn, connect it to an AI model, and send your first message.
---

Nocturn is a single program with nothing to install alongside it. No runtime, no database.
Download it, connect it to an AI model, and you are talking to your assistant.

## 1. Download

Grab the build for your system from the [releases page](https://github.com/efuturetoday/nocturn/releases)
and make it runnable:

```bash
# macOS / Linux
chmod +x nocturn
./nocturn
```

On Windows, run `nocturn.exe`.

That is the whole install. You can keep the file anywhere. Nocturn stores its data in a
`workspaces/` folder next to wherever you run it.

## 2. Connect an AI model

Nocturn does not include an AI model. It connects to one you provide, through the widely
used OpenAI-compatible API. Most providers support this, whether a hosted service or a model
you run yourself, so you can bring whichever one you like.

Create a file named `.env` in the folder where you run Nocturn, and add your provider's
details:

```ini
FREELLM_API_KEY=your-key-here
FREELLM_BASE_URL=https://your-provider.example/v1
```

- `FREELLM_API_KEY` is the API key from your model provider.
- `FREELLM_BASE_URL` is that provider's OpenAI-compatible endpoint.
- Optionally, `FREELLM_MODEL` picks a specific model. The default is `auto`.

The `FREELLM_` prefix is simply the variable name Nocturn reads. Any OpenAI-compatible
provider works. That is the only setup required to start chatting.

## 3. First run

Start Nocturn again:

```bash
./nocturn
```

The first time, it asks you to choose a **master passphrase**. This locks a small encrypted
vault where Nocturn keeps things like sign-in tokens, so nothing sensitive is ever stored in
the clear. One master passphrase opens every workspace's vault, and you enter it once each
time you start up.

Keep this passphrase safe. It cannot be recovered. If you lose it, Nocturn cannot open the
vault, and you set up your connected accounts again from scratch.

Then the chat opens. Type a message and press **Enter**:

> summarize what's in my notes

Nocturn streams its answer back. When it needs to *do* something, like send a message,
write a file, or reach a new website, it stops and asks you first. That pause is the whole
point. See [Approvals](/guides/approvals/).

## What's next

- [The playground](/guides/the-tui/): the keys and shortcuts you will actually use.
- [Approvals](/guides/approvals/): how to approve from your phone.
- [Plugins](/guides/writing-plugins/): connect the assistant to email, APIs, and more.

:::tip[Where your stuff lives]
Everything Nocturn knows is a folder you can copy, back up, or move to another machine. See
[The workspace](/guides/the-workspace/).
:::
