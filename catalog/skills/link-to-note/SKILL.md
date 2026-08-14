---
name: link-to-note
description: Read a link and file it as a durable note. Use when the user sends a URL and says to save it, remember it, or read it later.
---
# File a link as a note

1. `time_now`, so the note carries the date it was filed rather than a vague "recently".
2. `http_read` the URL. If it cannot be read, say so and file nothing — an empty note about a page
   nobody could open is worse than no note.
3. Distil it: what it is, the three or four points worth having later, and one line on why it matters
   to this user. Not a full summary — this note is read back in six months by someone who wants to
   remember whether to open the link again.
4. `memory_write` to `reading/<slug>.md`:
   - `summary` — one line naming the thing, e.g. "postgres index tuning, long read, keep". It is what
     the memory catalog shows in every prompt, so it identifies the note; it does not tease it.
   - `content` — the source URL on its own line, the date, the bullets, the why.
5. Tell the user the path you wrote, so they can find it without asking you.

If a note on the same source already exists, read it and extend that one rather than filing a second.
Two notes on one link is how a memory folder stops being trustworthy.
