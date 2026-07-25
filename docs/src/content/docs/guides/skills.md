---
title: Skills
description: Bundled know-how that shapes how the assistant works — and grants it no power at all.
---

A skill is know-how in a folder. It teaches the assistant *how* to approach a kind of task — a
checklist, a house style, a sequence of steps. It grants **no** new ability: a skill is text, and
text cannot widen what the assistant may do.

## Write one

A skill is a folder in your workspace with a `SKILL.md`: a short frontmatter block, then the
instructions.

```markdown
---
name: summarize-url
description: Fetch a page and write a tight summary
---

Fetch the page the user gives you. Write a summary of at most five sentences.
Lead with the single most important point. Skip navigation and ads.
```

Drop the folder in `skills/` and it is available at the next start. As with agents and plugins, the
folder name is the identity; a `name` in the frontmatter that disagrees is warned about. A skill
folder without a `SKILL.md` is simply not a skill, and an unparseable one is skipped with a
diagnostic rather than breaking the others.

## How a skill gets used

The `description` of every skill is visible to the assistant from the start; the body is not. When a
task matches, the assistant loads the one it needs (`skill_load`) and follows it from there. You do
not have to name a skill for it to be used — that is the point of keeping descriptions cheap and
bodies out of the way.

A skill can bundle extra files beside its `SKILL.md` — reference notes, a template, an example. The
assistant reads those with `skill_read`, and only from that skill's own folder.

Neither tool is gated, because neither carries authority: they add context, never reach. What the
assistant does *after* reading a skill is gated exactly as before.

## Why a skill grants no power

This is the part that makes third-party skills safe to use. A skill can say "email the summary to
the team" — and the send still asks you, because the ask comes from the [gate](/reference/gate/),
which never sees the skill. The worst a hostile skill can do is give bad advice. It cannot act.

Compare that with a [plugin](/guides/writing-plugins/), which does add tools and therefore is a
trust decision. Skills are the safe half of extending the assistant; plugins are the half worth
reading first.

Skills live in the control plane, outside `mnt/`, so the assistant can read them but cannot write or
edit them. It cannot author its own instructions.
