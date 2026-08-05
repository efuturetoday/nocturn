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

| Field | Meaning |
|---|---|
| `name` | Optional. **The frontmatter wins here**, and the folder is only the fallback — the one place in Nocturn where it works that way. See below. |
| `description` | What the assistant sees from the start, for every skill, on every turn. This is the whole reason it will ever load the body, so write it as the answer to "when would I need this?" |
| the body | Everything after the frontmatter: the instructions themselves, in prose. Read only when the skill is loaded, and capped at 256 KiB. |

Unknown frontmatter fields are **ignored**, not rejected — the opposite of a
[plugin manifest](/nocturn/guides/writing-plugins/), where a stray field is an error. The difference
is authority: a mistyped field in a manifest could silently drop a permission, while a skill has no
permissions to get wrong.

The same reasoning makes the name work backwards from every other kind. An agent, a plugin and an
MCP server take their identity from their folder, because the folder is what you reviewed when you
put it there. A skill carries **zero authority** — no credential owner, no vault shard, no tools —
so nothing hangs off its identity, and it follows the agentskills.io convention of naming itself in
`SKILL.md` instead.

Drop the folder in `skills/` and it is available at the next start. A folder without a `SKILL.md` is
simply not a skill; an unparseable one is skipped with a diagnostic rather than breaking the others.

If the folder holds other files besides `SKILL.md`, a listing of them (up to 40) is appended to the
body, so a loaded skill tells the assistant what it can go on to
[`skill_read`](/nocturn/reference/tools/skill_read/).

## Putting one in, end to end

```sh
cp -r examples/workspace/skills/summarize-url \
      nocturn-data/workspaces/main/skills/summarize-url

nocturn serve        # start, or restart if it was already running
```

Like plugins and MCP servers, skills are read when a workspace opens — there is no watcher on
`skills/`, so a running daemon does not notice a new folder.

Unlike them, nothing else happens. No credential to connect, no manifest to review, no restart
worth being careful about: from that point the assistant simply knows the skill's `description`, and
reaches for the body when a request matches it.

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
the team" — and the send still asks you, because the ask comes from the [gate](/nocturn/reference/gate/),
which never sees the skill. The worst a hostile skill can do is give bad advice. It cannot act.

Compare that with a [plugin](/nocturn/guides/writing-plugins/), which does add tools and therefore is a
trust decision. Skills are the safe half of extending the assistant; plugins are the half worth
reading first.

Skills live in the control plane, outside `mnt/`, so the assistant can read them but cannot write or
edit them. It cannot author its own instructions.
