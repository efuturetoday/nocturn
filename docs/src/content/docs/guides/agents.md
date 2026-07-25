---
title: Agents
description: Define an agent, let it work in the background, and decide up front what it may do without you.
---

An agent is a job you hand off. You describe what it should do and which tools it may use, then let
it run — from the chat, or on a schedule. What happens when it wants to do something that needs a
human is decided in advance, by one setting, and the safe value is the one you get by forgetting to
set it.

## Define an agent

An agent is a folder in your workspace with one file, `agent.md`: a YAML settings block, then the
brief in plain language.

```markdown
---
name: morning-briefing
description: Summarize my inbox and today's headlines
tools:
  - http_read
  - file_write
autonomy: guarded
when: cron("0 7 * * *")
effort: high
budget: 5m
---

Read my inbox in mnt/inbox.txt and the headlines from the news sites I follow.
Write a short summary to mnt/briefing.md. Keep it under ten bullet points.
```

| Field | What it does |
|---|---|
| `name` | Optional. The folder name is the identity; a differing `name` here warns, and the folder wins. |
| `description` | One line, shown by `/agents` and `nocturn ls`. |
| `tools` | The agent's **cage** — the tools it has at all. Anything not listed does not exist for it. |
| `when` | `cron("0 7 * * *")` to run on a schedule. Leave out to run only when asked. |
| `autonomy` | `strict` (default) or `guarded`. See below. |
| `effort` | Reasoning effort: `low`, `medium`, `high`, `xhigh` — endpoint-dependent. |
| `budget` | A Go duration (`5m`, `90s`) bounding the run. |

Two failures are deliberately loud: a `budget` that does not parse and an `autonomy` that is not one
of the two values are **hard errors**, and the agent is skipped rather than run with the wrong bound
or a looser dial than you meant. Unknown fields are ignored — so a `model:` line does nothing today.

## How independent it is

`autonomy` decides what happens when an agent, running with no human in front of it, hits an action
the gate wants to ask about:

| Setting | What happens |
|---|---|
| `strict` *(default, and the zero value)* | No approver is wired. The ask is **denied**, and the run says so. |
| `guarded` | The ask is routed **out of band** — a push wakes your phone, and the run waits for your answer. |

There is no third setting, and in particular there is no "just do it". The most an agent can be
granted is the right to *ask you somewhere else*.

Note what `strict` being the zero value buys: a missing dial, a typo'd file, an agent you forgot to
configure — all of them fail toward less authority. And `guarded` with no paired device collapses
back to `strict`, because there is nobody to ask. Setting up remote approval is what *adds*
capability; skipping it never silently grants any.

:::caution[Why there is no free-running mode]
An agent reads untrusted content, and untrusted content tries to hijack it. That is not a
hypothetical — it is the main threat this whole design exists for. `guarded` keeps you in the loop
for exactly the moments that matter. See [the threat model](/architecture/threat-model/).
:::

## Run it

**From the chat:**

```
/agents                                  # list them
/fire morning-briefing check only the inbox   # run one now, with a one-off task
```

The run happens in the background and streams into your terminal marked with its run id, while you
keep typing.

**On a schedule:** with a `when: cron(...)` line the agent fires by itself. Scheduling lives in the
process, so Nocturn has to be running — either your terminal session or
`nocturn serve`. Nothing fires while the program is closed; a missed window is missed, not queued.

## What an agent may reach

Two independent limits, and it is worth keeping them apart:

- **`tools`** is the cage — which tools exist for this agent at all. Not listed means not present,
  which no prompt can talk its way around.
- The **gate** still applies to every call the agent makes, exactly as it does for you. An agent
  with `http_read` in its cage still needs the host to be approved or granted.

An agent's settings can only tighten what you already allowed; nothing in `agent.md` widens it.

## Good to know

- Every run leaves its transcript behind in `agent-runs/`, so you can read exactly what an agent
  read, did and answered.
- Runs start fresh. An agent carries no memory from one firing to the next — what it should know has
  to be in its brief or in `mnt/`.
- The persona in `PERSONA.md` shapes the workspace's assistant, not its agents. Each agent is
  defined entirely by its own instructions.
