---
title: The workspace
description: An agent's isolated, portable world, where everything it can see and everything it remembers lives.
---

The workspace is the most important idea in Nocturn. Understand this and the rest follows.

A workspace is a single folder. It holds everything about your setup: the data your agents
work with, the accounts they can use, the permissions you have granted, and the agents,
skills, and plugins themselves. There is no database and no hidden state. **The folder is
the whole thing.**

A workspace can hold several agents. They share this one folder: the same `mnt/` data and
the same connected accounts. What each agent keeps to itself is its own instructions, its
own list of allowed tools, and its own permissions.

## `mnt/` is everything the agent can see

Inside the workspace, one directory named `mnt/` is the agent's entire visible world. Its
files, its notes, and its working data all live there, and the agent can read and write
only inside it. It cannot see or touch anything outside `mnt/`.

This isolation is not a rule the agent is asked to follow. It is structural. The agent is
handed a view of `mnt/` and nothing else, so there is nothing to escape. A hostile
instruction cannot send it wandering through your home directory, because your home
directory was never in its world to begin with.

```
workspaces/default/
├─ mnt/          ← the ONLY thing the agent sees, its whole world
│  ├─ inbox.txt
│  ├─ notes/
│  └─ summary.md
│
│  ── everything below is yours, and invisible to the agent ──
├─ secrets.age   ← encrypted vault (connected accounts, tokens)
├─ grants.json   ← the standing permissions you have granted
├─ mcp.json      ← remote MCP servers you have connected
├─ agents/       ← the agents defined in this workspace
├─ skills/       ← bundled know-how
└─ plugins/      ← installed capabilities
```

Everything below `mnt/` is the control plane, the settings and secrets you manage. The
agent cannot read its own permissions file to grant itself more, and it cannot rewrite its
own instructions, because those files are not in its world. That is what keeps a hijacked
agent from quietly promoting itself.

## The whole state lives here

Because there is no database, the workspace folder is the complete picture:

- the agent's files and data, in `mnt/`,
- its connected accounts, safely encrypted,
- its standing permissions,
- its agents, skills, and plugins.

The only thing that lives outside the workspace is `.env`, the small file that holds your
model connection. That sits next to the program, not in the workspace, so remember it
separately when you move a workspace to another machine.

## Portable and versionable by design

This is the payoff. Since the folder is the entire state, you can treat it like any other
project folder:

- **Copy it** to another machine and the agent comes with it: same memory, same
  permissions, same abilities.
- **Version it** with git to track how it changes over time and roll back mistakes.
- **Back it up** by copying one directory.

An agent is not tied to the machine it was born on. It is a folder you own and move around.

:::note[One secret stays out of the copy]
The vault (`secrets.age`) is encrypted with your passphrase. Copying the folder copies the
locked vault, and the tokens inside cannot be read without you. See
[Secrets](/guides/connecting-accounts/).
:::

## Where it is on disk

By default, Nocturn uses `workspaces/default/`, created next to wherever you run the
program. Support for multiple workspaces on one machine is on the way.
