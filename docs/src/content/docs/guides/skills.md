---
title: Skills
description: Teach the assistant how to handle a kind of task, without giving it any new power.
---

A skill is bundled know-how. It teaches the assistant *how* to approach a certain kind of
task: a checklist, a house style, a set of steps. A skill does not give the assistant any
new abilities. It only shapes how the assistant uses the tools it already has, so every
action still goes through approval.

## Write a skill

Each skill is a folder in your workspace with one file, `SKILL.md`. A short block at the
top names and describes it. The rest is plain instructions.

```markdown
---
name: summarize-url
description: Fetch a page and write a tight summary
---

Fetch the page the user gives you. Write a summary of at most five sentences.
Lead with the single most important point. Skip navigation and ads.
```

That is the whole skill. Drop the folder in `skills/` and it is available.

## Use a skill

You turn a skill on by name from the [playground](/guides/the-tui/):

```text
/summarize-url
/summarize-url https://example.com/article
```

`/skills` lists what is available. The assistant can also pull in a skill on its own when a
task calls for it, so you do not have to name one every time.

A skill can bundle extra files next to it, like reference notes or a template. The
assistant reads those only when the skill is active, and only from that skill's folder.

## Why skills grant no power

This is the important part. A skill is text, not a permission. It can tell the assistant to
send an email, but the send still asks you first. Nothing in a skill can widen what the
assistant is allowed to do. That is why you can use skills written by other people safely:
the worst a bad skill can do is give poor advice, never take an action on its own.

Because skills live outside the agent's [workspace view](/guides/the-workspace/), the
assistant cannot write its own skills or change them. You control them, the same way you
control its permissions.
