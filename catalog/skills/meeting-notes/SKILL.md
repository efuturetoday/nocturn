---
name: meeting-notes
description: Turn raw meeting notes into decisions, owners and action items. Use when the user pastes notes from a call or asks what came out of a meeting.
---
# Meeting notes

1. Work only from what you were given. A meeting you were not in has no context you can supply.
2. Sort it into three blocks, in this order, and leave a block out entirely when it is empty:
   - **Decided** — what was actually settled. Each line states the decision, not the discussion.
   - **To do** — one line per action: what, who owns it, by when. Write `owner: unclear` or
     `due: unstated` when the notes do not say. Guessing an owner is how an action item dies.
   - **Open** — questions nobody answered, and what would settle each one.
3. Discussion that led nowhere does not get a block. Say "no decision on pricing" in the open block
   rather than reproducing the argument.
4. Offer reminders, do not create them: show the list first, and only after the user says yes call
   `remind` once per dated action (`when` as an RFC3339 timestamp, message naming the action and its
   owner). An action with no date gets no reminder — ask for the date instead of inventing one.

Names as the notes wrote them. Do not promote "Sam mentioned" into "Sam will".
