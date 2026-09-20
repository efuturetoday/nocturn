---
name: weekly-review
description: Pull the week together — what happened, what is still open, what matters next. Use when the user asks for a weekly review, a retro, or what they got done this week.
---
# Weekly review

1. `time_now` first, and state the window you are reviewing ("Mon 4 – Sun 10 August"). A review with
   no window is a mood.
2. Gather:
   - the memory notes whose catalog lines touch this week — read them with `memory_read`, not all of
     them, only the ones that plausibly bear on it;
   - `remind_list` for what is still pending, and note anything whose time has passed.
3. Write four short blocks:
   - **Done** — what actually closed. If you cannot tell from the notes whether something finished,
     it belongs in "open", not here.
   - **Open** — still running, with what it is waiting on.
   - **Slipped** — things that were due and did not happen. Name them plainly, without commentary.
   - **Next week** — the one or two things that would make the week count. Not a list of everything.
4. Offer to file it: `memory_write` to `reviews/<year>-w<week>.md` with a summary line like
   "week 32: shipped the catalog, hiring still open". Only after the user has read it and said yes —
   a review they have not seen is not a review of their week.

The value is in what you leave out. A twelve-line review of a normal week is the correct length.
