---
title: Memory
description: The notes the assistant keeps about you — one fact per file, a catalog folded into every prompt, and the one write that asks when nobody is watching.
---

A chat ends and takes everything with it. Memory is the part that does not: a folder of notes the
assistant writes about you, so tomorrow's conversation starts knowing your partner's name, that you
prefer tabs, and which project you have been complaining about for a month.

It is deliberately small, and deliberately readable. Not a database and not embeddings — Markdown
files you can open in any editor, correct by hand, and delete.

## One fact, one file

```
nocturn-data/workspaces/main/memory/
  people/lina.md
  people/oliver.md
  prefs/coding.md
  projects/nocturn.md
```

Folders are yours to invent; the assistant groups related notes the way you would. Every note
carries a one-line summary in its frontmatter, and a write **replaces the whole file** rather than
appending to it — so a fact that changed is corrected, not buried under its older version.

The summary is not something the model spells into the file itself. It is a separate argument on the
tool, so the tool owns the serialization: a note can neither arrive without a catalog line, nor
carry a hand-built YAML header that one stray colon would break.

## The catalog is derived, and it is capped

Every turn, the assistant's system prompt carries a catalog — one line per note, `path — summary`:

```
people/lina.md — Lina, partner. Vegetarian, allergic to walnuts.
prefs/coding.md — Prefers Go, tabs, and short functions. Dislikes frameworks.
projects/nocturn.md — Building a secure personal assistant in Go. Ships nights and weekends.
```

**That catalog is not a file.** It is walked fresh from the folder on every turn, which is why
editing a note in your text editor takes effect in the next message with nothing to re-index and
nothing to keep in step.

It is capped at **2 KiB** — around fifty entries — and the cap is *enforced*, not requested. Past
it, entries are dropped and the drop is stated in the prompt itself:

```
(12 more notes not listed — memory is at its limit; consolidate)
```

A full memory reads as full rather than as complete. That ceiling is the point: the catalog is paid
for in every turn, forever, so the pressure to merge stale notes instead of piling them up is
structural rather than a rule the model is asked to follow. The notes themselves are uncapped —
they cost nothing until read.

An empty folder puts **nothing** in the prompt. A fresh workspace pays no tokens for a feature it is
not using.

## Two tools, two very different risks

| Tool | Gate |
|---|---|
| [`memory_read`](/nocturn/reference/tools/memory_read/) | **ungated** — reading back your own stored text is context, not authority. The same argument that leaves `skill_read` ungated. Capped at 64 KiB per read, and scanned for secrets on the way in. |
| [`memory_write`](/nocturn/reference/tools/memory_write/) | gated on the [`memory`](/nocturn/reference/gate/memory/) kind, with the note's path as the target |

The catalog only appears in a prompt when the runner can actually reach memory — an agent whose cage
holds neither tool never sees your notes. A [spoken session](/nocturn/guides/speaking/) gets
`memory_read` and deliberately not `memory_write`: a room is not a place where a decision to
remember something should be taken.

### The one gate that depends on who is watching

`memory` is the only kind whose answer changes with the situation:

- **In a chat**, a write is **allowed** and shown in the transcript as it happens. Asking would buy
  you "before" instead of "after" and nothing else — you are already looking.
- **In an unattended agent run**, it **asks**, out of band, on your phone. A cron agent firing at
  6am writes into the store folded into *every* future prompt, in every conversation, with nobody
  reading its transcript. With no device paired, the missing approver denies it, fail-closed.

A grant is remembered against the note's path, and can be widened to the containing folder —
approving `people/lina.md` can be broadened to `people/*` once, rather than answered per person.

### Secrets are checked before you are asked

The egress scan runs **before** the gate, not after. If the text about to be stored carries a value
from your vault, the write is refused outright — so a credential never even appears in the approval
dialog you were about to read.

## Why it lives outside the mount

The `memory/` folder is a **sibling** of the file tools' root, not inside it
([ADR-10](https://github.com/efuturetoday/nocturn/blob/main/ADRS.md)). `file_write` cannot reach it,
`file_read` cannot list it, and `code_run` has no path to it. The only writer is `memory_write`, and
that one goes through the gate.

That matters more than it first looks. The catalog is injected into every prompt, which makes memory
the most valuable thing on disk for a [prompt injection](/nocturn/architecture/threat-model/) to
reach: text written there is read back, by the assistant, forever. Confinement by construction —
the path is simply not in the mount — is what removes the whole class, rather than a rule that could
be wrong.

## What it should and should not keep

The tool tells the model this directly, so it is worth knowing what it was told.

**Remember:** names and relationships, standing preferences, ongoing projects, and corrections you
have given it.

**Do not remember:** anything already in the current conversation, anything read off a web page or a
document — that is not you telling it something — and one-off requests.

That last exclusion is the load-bearing one. A summary of a page the assistant just fetched is
exactly how untrusted text becomes durable context that nobody looks at again.

## Editing it yourself

It is a folder. Open it, fix a wrong fact, delete a note that is no longer true, or write one by
hand — the next turn picks it up. Nothing caches, nothing needs rebuilding.

Deleting the whole folder is a clean reset: the store treats a missing folder as an empty memory,
and the first write creates it again.
