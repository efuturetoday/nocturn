---
title: The TUI
description: The terminal chat where you try things out, watch the assistant work, and approve what it wants to do.
---

Start Nocturn with no arguments and you land in a chat:

```
$ nocturn
nocturn (model "gemini-3.5-flash") — /chats · /open <id> · /new · /agents · /fire <name> <task> · /quit

type a message to start a chat.

>
```

It is a plain line-based prompt: type a message, press Enter, watch the answer stream back. No
alternate screen, no key bindings to learn — which also means no scrollback of its own, since your
terminal already does that job.

## Watching it work

The reply streams as it is produced. Two other things show up in the stream:

- **Reasoning** appears **dimmed**, before the answer, when the model thinks first.
- **Tool calls** print as they happen, with their arguments, so nothing touches the world invisibly:

```
  → http_read({"url":"https://example.com"})
```

A failing tool prints its error the same way (`← http_read: …`). Each turn ends with its token
count:

```
[tokens: 1843]
```

The same conversation is readable from the [companion app](/nocturn/guides/the-app/) — conversations
belong to the workspace, not to the device you started them on:

![The same kind of turn in the app: the question, a chip naming the `time_now` tool, and the answer underneath.](../../../assets/screenshots/app-chat-time-now.jpg)

## Approving an action

When a tool needs your decision, the prompt appears inline and the turn waits:

```
  [approve] net → api.github.com ? [y=session / a=always / 1=always *.example.com / N]
```

| Answer | Meaning |
|---|---|
| `y` | yes, and remember it **for this session** |
| `a` | yes, and remember it **always** — written to `grants.json` |
| `1`, `2`, … | yes, and remember the **widened** target that is offered (here `*.example.com`) |
| anything else | no |

Note the default: anything you did not explicitly approve is a no, including just pressing Enter.
What is being approved is the `{kind → target}` pair, never "this tool" in general — see
[cage and gate](/nocturn/reference/gate/).

## Commands

Type these instead of a message:

| Command | What it does |
|---|---|
| `/chats` | List the conversations in this workspace |
| `/open <id>` | Continue an existing conversation |
| `/new` | Start a fresh conversation — the next message begins it |
| `/agents` | List the agents in this workspace |
| `/fire <name> <task>` | Run an agent now, in the background, with `task` as its job |
| `/quit`, `/exit` | Leave |

`/new` does not delete anything: the old conversation keeps living in the workspace and you can
`/open` it again by id. Session-scoped approvals belong to the running process, not to the
conversation, so they survive `/new` and end when you quit.

## Agents run alongside you

`/fire` starts an agent in the background and its output streams into the same terminal, marked with
its run id, while you keep typing:

```
  [agent run-4f2a] → file_write({"path":"briefing.md",…})
[agent run-4f2a done]
```

A background run never releases your prompt — the two do not fight over the input line. What an
agent may do on its own is its `autonomy` setting; see [Agents](/nocturn/guides/agents/).

## Other subcommands

The chat is what you get with no arguments. The binary does a few other things:

| Command | What it does |
|---|---|
| `nocturn serve` | Run the daemon the [companion app](/nocturn/guides/remote-access/) talks to |
| `nocturn ls` | List workspaces |
| `nocturn secret set <target>` · `nocturn secret ls` | Manage vault credentials |
| `nocturn auth <provider>` | Run an OAuth flow and store the token |
| `nocturn version` · `nocturn help` | The obvious |

Most of them take `-w <workspace>` (default `main`).
