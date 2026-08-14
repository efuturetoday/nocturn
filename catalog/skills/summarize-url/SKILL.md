---
name: summarize-url
description: Fetch a web page and summarize it. Use when the user gives a URL and asks what it says, for a summary, or for the key points.
---
# Summarize a URL

1. Read the page with the `http_read` tool (GET the given URL). The response is a JSON envelope —
   the page text is in `body`, and `status` says whether you got the page at all.
2. If the response is HTML, extract the readable text — ignore tags, scripts and navigation.
3. Produce a tight summary: three to five bullet points of the key claims, then one sentence of
   overall takeaway.
4. Keep it faithful. Do not add facts the page does not state, and say so when the page is mostly
   navigation or a paywall rather than inventing content for it.
