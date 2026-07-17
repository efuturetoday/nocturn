---
title: The playground (chat)
description: The interactive chat where you try things out and shape an agent before letting it run on its own.
---

When you start Nocturn you land in a chat. Think of it as a playground. It is where you try
tasks, watch how the assistant handles them, and shape an [agent](/guides/agents/) before
you let it run in the background. It is also the simplest way to just ask for something on
the spot.

Here is what you need to drive it comfortably.

## Everyday keys

| Key | What it does |
| --- | --- |
| **Enter** | Send your message |
| **Shift+Enter** (`Ctrl+J`) | Add a new line without sending |
| **Esc** | Stop the assistant mid-answer |
| **Ctrl+N** | Start a fresh session |
| **Ctrl+C** | Quit |
| **PgUp / PgDown** | Scroll back through the conversation |

**Ctrl+N** starts over with a clean slate. It clears the chat and forgets any "allow this
session" approvals you had given. Things you allowed *always* still stick.

## Watching it work

As the assistant answers, it streams the text in real time. When it uses a tool, a small
indicator shows what is happening:

- a **spinner** while it is working,
- a **✓** or **✗** when it finishes,
- a **⌛** when it is paused, waiting for you to approve something.

You always see what it touched, such as which site it fetched or which file it read, so
nothing happens invisibly.

## Typing ahead

You do not have to wait for the assistant to finish. Type your next message while it is
still working and press **Enter** — it is **queued** (shown dimmed with a ⏳) and sent
automatically as soon as the current turn ends. Queue several and they are sent in order.

The same thing happens when the assistant schedules itself to carry on later (a short
"pick this back up in a few minutes"): that resumption joins the queue and runs once the
current turn is done, so two turns never overlap.

## Slash commands

Type these at the start of a message:

| Command | What it does |
| --- | --- |
| `/skills` | List the skills available in this workspace |
| `/agents` | List the agents available in this workspace |
| `/ws` | List your workspaces; `/ws <name>` switches the one you are talking to |
| `/<name>` | Run the skill or agent with that name; you can add text after it as a task or input |

For example, `/summarize-url https://example.com` runs a skill, and `/morning-briefing
check only the inbox` runs an agent. If a skill and an agent happen to share a name, the
agent runs.

**Skills** are bundled know-how that guides *how* the assistant tackles a kind of task.
They give it no new powers, so it still asks before acting. **Agents** are separate helpers
with their own focus and their own permissions. You will find both in your workspace
folder. See [The workspace](/guides/the-workspace/).

## Starting fresh vs. carrying on

A session is one continuous conversation. Within it, the assistant remembers what you have
discussed and the approvals you granted for the session. Press **Ctrl+N** whenever you want
a clean context, either for a new topic or to drop session approvals.
