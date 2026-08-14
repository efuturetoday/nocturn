---
name: daily-brief
description: Give the morning round-up — the date, what is due, what is worth knowing. Use when the user asks for a briefing, for "what does today look like", or when a scheduled agent runs this.
---
# Daily brief

Written to be read once, on a phone, before coffee. Everything here is optional except honesty about
what you could not find out.

1. `time_now` — the date and the weekday. Everything below is relative to it, so it comes first.
2. `remind_list` — what is pending. Today's and tomorrow's items belong in the brief; a reminder three
   weeks out does not.
3. What you already know about the user from memory: a trip, a deadline, a person they meant to call.
   Read a note with `memory_read` only when the catalog line suggests it bears on today.
4. Weather, if the `weather` skill is installed or you can reach Open-Meteo — one clause, not a
   forecast.

Then write it:

- One line of orientation: the day, the date, the weather in a handful of words.
- **Due today** — the reminders, each in its own line, soonest first.
- **Worth knowing** — at most two things, only if they are actually due to matter today.
- Nothing else. No greeting, no "here is your brief", no encouragement.

A quiet day is a two-line brief and that is the correct output. Never pad it with something you
inferred, and never turn "I found nothing" into "you have nothing on" — those are different claims.
