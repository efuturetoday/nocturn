---
title: The command line
description: Every nocturn subcommand, what it needs, and which of them talk to a running server.
---

One binary, and everything below is it. `nocturn help` prints the same list; this page adds what the
help has no room for — what each command needs before it will work, and whether it touches a running
server or the files on disk.

```
nocturn                      Open the interactive terminal assistant
nocturn serve                Run the WebSocket server
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
| `nocturn` | The terminal assistant, in the default workspace. Also starts that workspace's **cron agents** — scheduling lives in the process, not in the server. |
| `nocturn serve [--host] [--port] [--no-web]` | The [WebSocket server](/nocturn/guides/remote-access/) the app, the browser UI and satellites connect to. `--host` picks the interface (empty = all, `127.0.0.1` = this machine only), `--port` the port; `NOCTURN_HOST` and `NOCTURN_PORT` set the defaults. `--no-web` serves the protocol without the browser UI. This is what keeps agents running once you close the terminal. |
| `nocturn pair [--open] [--addr :8080]` | Mint a pairing code on the **running** server and print it with a one-click link. Reads the server's own 0600 credential, so it works whenever the server is up — including over SSH on a headless box, long after the code printed at startup expired. `--open` launches a browser. |
| `nocturn voice [--port 8788] [-w workspace]` | A **PoC harness**, in its own words: a browser page for testing the voice path on loopback, with no pairing. Not a way to use Nocturn. |

`NOCTURN_CATALOG_URL` points the server at a curated catalog of skills and MCP servers, which the app
then browses and installs from. Unset — the default — the library is **absent**, not empty: nothing is
fetched and no request leaves the machine for it. See [Skills](/nocturn/guides/skills/) and
[Remote MCP servers](/nocturn/guides/remote-mcp/).

## Voice

| Command | What it does |
|---|---|
| `nocturn enroll --device <name> [--seconds 60] [--addr :8080]` | Asks a **running server** to have that satellite record its microphone, so a voice can be enrolled from the room and channel it will later be recognised in. The ring goes steady red while it records. |
| `nocturn voices ls [-w workspace]` | Who this workspace can recognise. |
| `nocturn voices add [--device <name>] [-w ws] <person> <files or dirs…>` | Enrol from 16 kHz mono WAVs — what the uplink already writes. `--device` is **required and never guessed**: a voice through a phone and through a hallway speaker are two channels. Needs `NOCTURN_SPEAKER_MODEL`. |
| `nocturn voices rm <person> [-w workspace]` | Forget a voice entirely. |

`voices` edits `voices.json` directly and talks to no server; one started afterwards picks it up.
`enroll` is the opposite — it needs a server, because the microphone is on the other side of it. See
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
open (`NOCTURN_MASTER_PASSPHRASE`), and none of them needs a running server — they read the workspace
folder directly. Where the value then lives is on [the vault](/nocturn/guides/vault/).

## Looking around

| Command | What it does |
|---|---|
| `nocturn ls` | The workspaces on this machine. |
| `nocturn ls -w <workspace>` | That workspace's plugins, MCP servers, agents and skills — what it actually loaded, which is the quickest way to see whether a folder you dropped in was picked up. |

## Inside the terminal assistant

`nocturn` with no arguments is a full-screen surface and needs a real terminal; piped into anything
it refuses and exits `2`. Everything it can do is behind `Ctrl+P`, the command palette. The slash
commands remain for typists and are typed into the composer rather than at the shell:

| | |
|---|---|
| `/chats` | Move the keyboard to the conversation list |
| `/new` | Start a fresh conversation |
| `/open <id>` | Reopen one by id — a chat or an agent run |
| `/agents` | Open the palette on the agents that can be fired |
| `/fire <name> <task>` | Run one now; `task` is optional |
| `/help` | Open the palette |
| `/quit`, `/exit` | Leave |

Anything else beginning with `/` is sent to the model.

The keys are in [The TUI](/nocturn/guides/the-chat/). The two that hold everywhere: `Ctrl+C` cancels
the running turn and never the program, `Ctrl+Q` leaves from any depth.

## What is deliberately missing

There is no command to install a plugin, a skill or an MCP server. **Putting the folder in the
workspace is the install**, and that is the whole authorization step — see
[plugins](/nocturn/guides/writing-plugins/). Nor is there one to grant a permission ahead of time: a
grant is created by answering an approval, never by a flag.
