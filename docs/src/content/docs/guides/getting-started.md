---
title: Getting started
description: Run Nocturn, connect it to an AI model, and send your first message.
---

Nocturn is a single program with nothing to install alongside it. No runtime, no database. Point it
at a model and you are talking to your assistant.

## 1. Get the binary

Grab the build for your system from the
[releases page](https://github.com/efuturetoday/nocturn/releases) and make it runnable:

```bash
# macOS / Linux
chmod +x nocturn
./nocturn
```

On Windows, run `nocturn.exe`. Or build from source with a Go toolchain:

```bash
go build ./cmd/nocturn
```

That is the whole install. Keep the file anywhere — Nocturn creates a `nocturn-data/` folder next to
wherever you run it, and everything it knows lives in there.

## 2. Connect an AI model

Nocturn ships no model. It talks to one you provide over the OpenAI-compatible API, so a hosted
service and a model on your own machine look the same to it.

Create a `.env` file in the folder you run Nocturn from:

```ini
FREELLM_BASE_URL=https://your-provider.example/v1
FREELLM_API_KEY=your-key-here
FREELLM_MODEL=your-model
```

| Variable | Meaning |
|---|---|
| `FREELLM_BASE_URL` | your provider's OpenAI-compatible endpoint |
| `FREELLM_API_KEY` | the key for it |
| `FREELLM_MODEL` | which model to use — defaults to `auto` if unset |
| `FREELLM_REASONING_EFFORT` | optional: `low`, `medium`, `high`, `xhigh`; endpoint-dependent |

Real environment variables win over the `.env` file, so you can override one without editing
anything. The `FREELLM_` prefix is just the name Nocturn reads — any compatible provider works.

## 3. First run

```bash
./nocturn
```

The chat opens. Type a message and press Enter:

> summarize what's in my notes

The answer streams back. When the assistant wants to *do* something that reaches off the machine or
changes a file, it stops and asks — right there in the terminal. That pause is the point. See
[the chat](/guides/the-chat/) for what the prompt looks like and
[cage and gate](/reference/gate/) for what is being asked.

## 4. Optional: unlock the vault

Credentials for real services live in an encrypted vault, one per workspace, at
`nocturn-data/workspaces/<name>/vault.enc`. It is unlocked by a master passphrase read from the
environment:

```ini
NOCTURN_MASTER_PASSPHRASE=correct-horse-battery-staple
```

Without it, Nocturn runs fine — the vaults simply stay locked and no credential can be injected.
With it, one passphrase opens every workspace's vault, each under its own derived key. There is no
recovery: lose the passphrase and the stored credentials are gone, and you set the accounts up
again.

:::note
An interactive unlock prompt is meant to replace this environment variable. Today it is the
environment, which means the passphrase is as protected as the environment it sits in.
:::

## What's next

- [The chat](/guides/the-chat/) — commands, streaming, and the approval prompt.
- [The workspace](/guides/the-workspace/) — the folder that *is* your assistant.
- [Remote access](/guides/remote-access/) — the daemon and approving from your phone.
- [Plugins](/guides/writing-plugins/) — connect it to your own services.
