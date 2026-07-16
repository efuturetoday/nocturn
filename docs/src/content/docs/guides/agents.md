---
title: Agents
description: Define an agent, let it work in the background, and approve only the actions that matter.
---

An agent is a job you hand off. You describe what it should do and which tools it may use,
then let it run, either from the chat or on its own in the background. When it hits
something that needs your yes, it asks. The rest it just gets done.

## Define an agent

Each agent is a folder in your workspace with one file, `agent.md`. The top is a short
settings block. Below it, in plain language, is what the agent should do.

```markdown
---
name: morning-briefing
description: Summarize my inbox and today's headlines
tools:
  - http.read
  - file.write
autonomy: guarded
when: cron("0 7 * * *")
---

Read my inbox in mnt/inbox.txt and the headlines from the news sites I follow.
Write a short summary to mnt/briefing.md. Keep it under ten bullet points.
```

The settings you will use most:

- **`tools`** lists the abilities this agent may use. It cannot touch anything off the list,
  so an agent meant for reading cannot suddenly send.
- **`autonomy`** sets how independent it is, covered below.
- **`when`** sets when it runs on its own. `cron("…")` schedules it. Leave it out to run the
  agent only when you ask.

Each agent also keeps its own permissions, separate from the chat and from other agents.
Approving something for one agent never affects another.

## Run it

Two ways:

- **From the chat.** Type `/morning-briefing` to run it now, or `/morning-briefing check
  only the inbox` to give it a one-off task. `/agents` lists everything available.
- **On a schedule.** With a `when: cron(...)` line, the agent runs by itself in the
  background. You do not need to be watching, but Nocturn does need to be running. There is
  no always-on service yet, so if you close the program, scheduled agents do not fire until
  you open it again.

When a scheduled agent runs, it works quietly. If it needs approval for an action, that
request comes to you, on your phone if you have set that up, and the agent waits for your
answer without timing out.

## How independent should it be?

The `autonomy` setting decides what happens when an agent, running on its own, wants to do
something that would normally ask:

| Setting | Behavior when it wants to act |
| --- | --- |
| `guarded` *(default)* | Reads happen quietly. Every action asks you on a second device (out of band) and waits. The safe default. |
| `strict` | Actions are refused outright. Good for a read-only agent that should never change anything. |
| `full` | Actions go ahead automatically, still limited to the agent's allowed tools and reach. Use only for agents you fully trust on trusted input. |

Even at `full`, the most sensitive actions still ask, and that floor cannot be turned off.
These are the actions a plugin marks as high-stakes, such as sending money or deleting data.
No setting lets an agent exceed the tools and reach you gave it either. Autonomy only decides
how the asking is handled, never how far the agent can go.

:::caution[Why not just let it run free?]
Because an agent reads untrusted content, and that content can try to hijack it. `guarded`
keeps you in the loop for exactly the moments that matter, without pestering you for the
harmless reads. That balance is the point of Nocturn.
:::

## Good to know

- Give an agent a time budget with `budget: 5m` so a runaway task stops on its own. Time
  spent waiting for your approval does not count against it.
- The `model` setting is read but not yet applied. Every agent currently uses the model you
  configured globally. The field is there for a future per-agent override.
- An agent's settings can tighten its permissions but never loosen them past what you have
  allowed.

## Where agents run from

Today agents run while Nocturn is open, triggered manually or on a
[schedule](/guides/triggers/). Starting them from messaging apps or over a REST API is on
the way. See [Channels](/guides/channels/).
