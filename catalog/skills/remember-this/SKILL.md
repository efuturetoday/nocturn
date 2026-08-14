---
name: remember-this
description: Write something into memory so it is still useful months later. Use when the user says to remember something, corrects a fact about themselves, or states a lasting preference.
---
# Remember this

Memory is folded into every prompt as a catalog of summary lines, with bodies read on demand. That is
what makes these rules rules rather than tidiness: a bad summary line costs tokens in every single
turn and still tells nobody anything.

1. **Check first.** If the catalog already has a line on this subject, `memory_read` it and update
   that note. A second note on the same fact means two answers to one question, and nothing says
   which is current.
2. **One subject per note.** Path by what it is about: `people/lina.md`, `projects/nocturn.md`,
   `prefs/writing.md`. Not by when you learned it.
3. **The summary line identifies, it does not tease.** "daughter, 7, allergic to hazelnuts" — not
   "notes about a family member". It is the only thing visible until the note is opened.
4. **Absolute dates.** "moves house on 2026-09-01", never "next month". The note outlives the
   conversation that produced it; use `time_now` when the user says something relative.
5. **Say what is durable, not what happened.** A preference, a constraint, a relationship, a decision
   and its reason. Not "asked me to summarize an article today".
6. **Never store a credential.** No passwords, tokens, keys or card numbers, even when handed one
   directly. Secrets belong in the vault, and a memory note is read into every prompt.
7. Tell the user the path you wrote and the summary line you used, in one line. They are the ones who
   have to be able to correct it later.

Wrong facts are worse than missing ones: when something you stored turns out to be wrong, rewrite that
note rather than adding a correction beside it.
