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

There is no watcher on `skills/`, so a running daemon does not notice a folder you copied in — it
reads them when the workspace opens. `nocturn reload` (`-w <workspace>` for another one) is how you
tell it to look again; it prints what the workspace holds afterwards. Restarting works too.

The other is the [companion app](/nocturn/guides/remote-access/), which manages skills over the same
connection everything else uses: it lists them, shows a skill's `SKILL.md` before you act on it, and
switches one off or removes it. A change made there takes effect **on the next message** — including
inside a conversation that is already open, and without interrupting a turn that is running. There is
nothing to restart.

Nothing else happens either way. No credential to connect, no manifest to review: from that point the
assistant simply knows the skill's `description`, and reaches for the body when a request matches it.

### From the catalog

Every daemon has a library: the app browses the catalog and installs an entry with one tap. Out of the
box that is the curated catalog this project publishes, built from `catalog/` in the repository —
`NOCTURN_CATALOG_URL` points the server at a different one, or at `off` for none. Nothing is fetched
until somebody opens the library, so a daemon whose owner never does talks to nobody about it.

The catalog is fetched by the server, from one host, over TLS, and it carries each skill's **whole
`SKILL.md` inline** — so installing never fetches from a second place, and the app can show you
exactly what will go into the assistant's prompt before anything is written.

The TLS is a requirement rather than a recommendation: a plain-HTTP catalog on a remote host is
refused, and so is a redirect that leaves the scheme or the host you configured. For a skill the
channel is the whole of what says these bytes are the catalog — the digest beside each entry is served
by the same host as the entry. A catalog on **this machine** is the exemption, and it is a path rather
than a URL: `NOCTURN_CATALOG_URL=./my-catalog.json` needs no web server and no TLS, because there is
no transport to secure.

Worth being straight about what that showing is and is not. It is informed consent, not a control:
nobody spots a subtle instruction buried in four thousand tokens on a phone. The controls that
actually hold are the ones already in the architecture — a skill carries [no authority at
all](/nocturn/architecture/threat-model/), and anything it talks the assistant into still meets the
gate. A skill is not signed, and that is the same judgement in another form: signing authenticates a
publisher, and a publisher's identity is worth paying for when what arrives is CODE. A catalog
[plugin](/nocturn/catalog/) is signed for exactly that reason.

The install command names a catalog **entry**, and cannot carry a skill body. That distinction is the
security of the whole feature: "install entry N of the catalog the server fetched" is a different act
from "put this text into every prompt", and only the first exists on the wire. Sideloading stays what
it always was — copying a folder on the host.

### A plugin may bring one

A [plugin](/nocturn/guides/writing-plugins/) can ship a `SKILL.md` beside its code, saying when to
reach for the tools it adds and what their arguments really take. It joins this same catalog and is
loaded the same way — the difference is that it has no folder of its own under `skills/`, so it
cannot be switched off or deleted: it arrives and leaves with its plugin. The Skills list shows it
anyway, marked with the plugin it came from, because it is in front of the model either way.

A skill you wrote or installed **wins** a name collision. A skill under `skills/` is something this
household chose on purpose, and an installed plugin must not be able to take its name over.

### Off is not gone

Switching a skill off moves its folder to `skills/.disabled/`, which the daemon skips — so it leaves
the catalog while everything you assembled stays where it is, bundled files included. Switching it
back on moves it back. Removing it deletes the folder; unlike a workspace there is no trash, because
a skill is instructions that came from somewhere and can come from there again.

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
