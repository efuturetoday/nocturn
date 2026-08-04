---
name: morning-briefing
tools: [http_read, knowledge_search, notify]
when: "0 7 * * *"
effort: low
autonomy: guarded
---
Every morning at seven, put together a short briefing.

1. Check the knowledge folder for anything with a date in the next two days — appointments,
   deadlines, renewal dates.
2. Fetch https://example.com and summarise it in two sentences.
3. Send the result with `notify`, as one paragraph. No preamble, no bullet list.

If something needs approval and nobody answers, say so in the notification rather than silently
skipping it.
