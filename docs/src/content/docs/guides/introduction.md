---
title: What is Nocturn?
description: Autonomous AI agents that work in the background and check in with you before doing anything that matters.
---

Nocturn runs autonomous AI agents for you. You give an agent a job and let it work: on a
schedule, in the background, on its own. When it wants to do something that matters, like
send a message, spend money, or change a file, it pauses and asks you. You can approve or
deny right from your phone.

That's the whole idea:

> Let agents work. Approve only what matters.

A quick word on terms. The **assistant** is Nocturn itself, the thing you talk to. An
**agent** is a specific job you set up for it to carry out, either on demand or on a
schedule. You will see both words throughout these guides.

The chat you get on first run is a playground. It is where you try things out and shape an
agent. The real value comes later, from agents that keep working when you are not watching
and only interrupt you when a decision is genuinely yours to make.

## Why it's built this way

An agent working on its own reads things you do not control: web pages, emails, incoming
messages. Any of those can hide instructions that try to hijack it into misusing the access
you gave it. Nocturn assumes this will happen, so it puts a gate in front of every real
action. A hijacked agent still cannot do anything irreversible without your explicit yes.
And because that yes lives on a second device, whatever hijacked it cannot answer for you.

## Two ideas to understand first

Everything else follows from these two.

### The workspace is the agent's whole world

An agent lives in a workspace, a single folder. Inside it, one directory named `mnt/` holds
every file the agent can see and touch on your computer. Its notes, its files, and its data
all live there, fully isolated. The agent cannot open any other file on your machine. It can
still reach the internet, but only through tools you gate, which is a separate matter from
what it can see on disk.

The workspace is also your entire setup. There is no hidden database. Copy the folder and
you have copied everything: the data, the permissions, the connected accounts, the agents,
and the skills. It is portable and versionable by design. Check it into git, back it up,
move it to another machine. [Learn the workspace](/guides/the-workspace/).

### You stay in control through approval

Agents do not get blanket permission. Reading is free, but anything that changes the world
waits for you. You decide once, for the session, or always, and the sensitive things always
ask. [Learn about approvals](/guides/approvals/).

## It runs on your machine

One program. No cloud account, no database, nothing to install alongside it. Download it,
point it at an AI model, and it is yours.

## Get going

- [Get started](/guides/getting-started/): download it and open the playground.
- [The workspace](/guides/the-workspace/): the agent's isolated, portable world.
- [Agents](/guides/agents/): put one to work in the background.
- [Approvals](/guides/approvals/): approve from your phone.
