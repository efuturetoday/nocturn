---
title: The command line
description: Every nocturn subcommand, what it needs, and which of them talk to a running daemon.
---

One binary, and everything below is it. `nocturn help` prints the same list; this page adds what the
help has no room for — what each command needs before it will work, and whether it touches a running
daemon or the files on disk.

```
nocturn                      Open the interactive terminal assistant
nocturn serve                Run the WebSocket daemon
nocturn voice                Run the browser voice PoC harness
nocturn enroll               Ask a satellite to record its microphone
nocturn voices ls|add|rm     Manage the voices a workspace can recognise
nocturn knowledge index|status|ls
                             Manage a workspace's document index
nocturn auth <provider>      Connect an OAuth account
nocturn secret set|ls        Seed and list static credentials
nocturn ls                   List workspaces, or one workspace's contents
nocturn version              Print the version
nocturn help                 Show the help
```

**`-w` / `--workspace` works on most of them** and defaults to `main`. A command without it acts on
the default workspace, not on all of them.

## Running it

| Command | What it does |
|---|---|
| `nocturn` | The terminal assistant, in the default workspace. Also starts that workspace's **cron agents** — scheduling lives in the process, not in the daemon. |
| `nocturn serve [--addr :8080]` | The [WebSocket daemon](/nocturn/guides/remote-access/) the app and satellites connect to. `NOCTURN_ADDR` sets the default. This is what keeps agents running once you close the terminal. |
| `nocturn voice [--port 8788] [-w workspace]` | A **PoC harness**, in its own words: a browser page for testing the voice path on loopback, with no pairing. Not a way to use Nocturn. |

## Voice

| Command | What it does |
|---|---|
| `nocturn enroll --device <name> [--seconds 60] [--addr :8080]` | Asks a **running daemon** to have that satellite record its microphone, so a voice can be enrolled from the room and channel it will later be recognised in. The ring goes steady red while it records. |
| `nocturn voices ls [-w workspace]` | Who this workspace can recognise. |
| `nocturn voices add [--device <name>] [-w ws] <person> <files or dirs…>` | Enrol from 16 kHz mono WAVs — what the uplink already writes. `--device` is **required and never guessed**: a voice through a phone and through a hallway speaker are two channels. Needs `NOCTURN_SPEAKER_MODEL`. |
| `nocturn voices rm <person> [-w workspace]` | Forget a voice entirely. |

`voices` edits `voices.json` directly and talks to no daemon; one started afterwards picks it up.
`enroll` is the opposite — it needs a daemon, because the microphone is on the other side of it. See
[the voice satellite](/nocturn/guides/speaking/).

## Knowledge

| Command | What it does |
|---|---|
| `nocturn knowledge index [-w workspace]` | Bring the index in line with the folder. Unchanged files are skipped, so re-running is cheap. |
| `nocturn knowledge status [-w workspace]` | How much is indexed, and where. |
| `nocturn knowledge ls [-w workspace]` | The documents currently in the index. |

Indexing **sends your documents to the configured embedding provider**, so it needs one — see
[knowledge](/nocturn/guides/knowledge/).

## Credentials

| Command | What it does |
|---|---|
| `nocturn auth <provider> [-w ws] [-scope "a b"]` | Runs an OAuth flow and stores the token. Prints a consent URL — it opens no browser. `<provider>` is an MCP server or a plugin's declared provider. `-scope` applies to discovery-mode MCP servers only. |
| `nocturn secret set <target> [-w workspace]` | Seeds a static credential, **value on stdin** so it stays out of your shell history and the process list. |
| `nocturn secret ls [-w workspace]` | The credential names this workspace holds — names only, never values. |

A target is owner-namespaced: `plugin:<name>/<credential>` or `mcp:<name>`. All three need the vault
open (`NOCTURN_MASTER_PASSPHRASE`), and none of them needs a running daemon — they read the workspace
folder directly. Where the value then lives is on [the vault](/nocturn/guides/vault/).

## Looking around

| Command | What it does |
|---|---|
| `nocturn ls` | The workspaces on this machine. |
| `nocturn ls -w <workspace>` | That workspace's plugins, MCP servers, agents and skills — what it actually loaded, which is the quickest way to see whether a folder you dropped in was picked up. |

## Inside the terminal assistant

Once `nocturn` is running, these are typed at the prompt rather than at the shell:

| | |
|---|---|
| `/chats` | List this workspace's conversations |
| `/new` | Start a fresh one |
| `/open <id>` | Reopen one by id |
| `/agents` | List the declared agents |
| `/fire <name>` | Run one now, in the background |
| `/quit`, `/exit` | Leave |

## What is deliberately missing

There is no command to install a plugin, a skill or an MCP server. **Putting the folder in the
workspace is the install**, and that is the whole authorization step — see
[plugins](/nocturn/guides/writing-plugins/). Nor is there one to grant a permission ahead of time: a
grant is created by answering an approval, never by a flag.
