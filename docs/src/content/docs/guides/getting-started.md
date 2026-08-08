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
OPENAI_BASE_URL=https://your-provider.example/v1
OPENAI_API_KEY=your-key-here
OPENAI_MODEL=your-model
```

| Variable | Meaning |
|---|---|
| `OPENAI_BASE_URL` | your provider's OpenAI-compatible endpoint |
| `OPENAI_API_KEY` | the key for it |
| `OPENAI_MODEL` | which model to use — defaults to `auto` if unset |
| `OPENAI_REASONING_EFFORT` | optional: `low`, `medium`, `high`, `xhigh`; endpoint-dependent |

Real environment variables win over the `.env` file, so you can override one without editing
anything. The `OPENAI_` prefix is just the name Nocturn reads — any compatible provider works.

### Beyond the chat (optional)

Two things need a second endpoint, and neither is needed to start:

- **[The voice satellite](/nocturn/guides/speaking/)** needs a live-audio model — a different kind of
  model, not a setting on this one. Work in progress, and configured on its own page.
- **[Knowledge](/nocturn/guides/knowledge/)** needs an embeddings endpoint to search documents you
  file in the workspace. Often the same gateway you just configured, in which case there is nothing
  more to set.

Leave both unset and everything on this page works as described.

## 3. Check that it works

```bash
./nocturn
```

The terminal chat opens. Type a message and press Enter:

> summarize what's in my notes

The answer streams back. When the assistant wants to *do* something that reaches off the machine or
changes a file, it stops and asks — right there in the terminal. That pause is the point. See
[cage and gate](/nocturn/reference/gate/) for what is being asked.

**This is the test drive, not the product.** The terminal chat is one process, one window, and it
only runs while you are sitting in front of it — which is the opposite of what Nocturn is for. Use
it to confirm your model works and to watch an approval happen once. Then move to the server.

## 4. Run it for real

```bash
./nocturn serve
```

That is the actual thing. The server holds your workspaces open, announces itself on your network so
[the app](/nocturn/guides/the-app/) can find it without an IP, and keeps running when you close the
laptop lid on the terminal you started it from.

Three things only exist here:

- **Agents on a schedule.** A cron agent firing at 6am is the whole idea — work that happens while
  you are asleep. Scheduling lives in the process, not in the server: a terminal session runs the
  same schedulers. What the server adds is outliving the terminal you would otherwise have to leave
  open.
- **Approvals when you are not there.** An unattended run that reaches the network or a file routes
  the ask to your phone. With no server there is no route, and a `guarded` agent falls back to
  refusing — which is safe and useless.
- **Everything that is not a keyboard.** The companion app, a voice satellite, anything that speaks
  the protocol. They all connect here.

The server prints a pairing code on its first start. That code is the one time a code comes from the
machine itself — every device after that is enrolled by a device you already trust. See
[the companion app](/nocturn/guides/the-app/) for the rest of that flow.

```bash
./nocturn serve --addr :8080     # the default; $NOCTURN_ADDR also works
```

Conversations are shared, not per device: start something in the terminal, pick it up on your phone,
finish it in the terminal again.

### Something to start from

The repository carries a workspace with one of everything — an agent, a skill, a plugin, an MCP
server, a memory note and documents to search:

```bash
cp -r examples/workspace nocturn-data/workspaces/demo
```

It is inert until you point it at something: the agent needs a model, the plugin needs the network
approved, the MCP server needs an account connected. Nothing in it holds a credential. `examples/`
has a walk-through of each piece and why it sits where it does.

## 5. Optional: unlock the vault

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

- [The companion app](/nocturn/guides/the-app/) — pair a phone, so an approval can reach you.
- [Agents](/nocturn/guides/agents/) — the work that runs while you are not watching.
- [The workspace](/nocturn/guides/the-workspace/) — the folder that *is* your assistant.
- [Knowledge](/nocturn/guides/knowledge/) — file documents, ask about them.
- [Remote access](/nocturn/guides/remote-access/) — the protocol, pairing, device classes.
- [Plugins](/nocturn/guides/writing-plugins/) — connect it to your own services.
- [The TUI](/nocturn/guides/the-chat/) — commands and the approval prompt, in the terminal.
- [The command line](/nocturn/reference/cli/) — every subcommand, and which need a running server.
