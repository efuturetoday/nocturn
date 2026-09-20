---
name: gmail
description: How to search and read the user's Gmail with the gmail_* tools. Use when the user asks about their mail, an email from someone, whether something arrived, or what is unread.
---
# Gmail

Three tools: `gmail_search` (find), `gmail_read` (open one), `gmail_labels` (which mailboxes exist).
Read-only — there is no send, and saying so is better than trying.

## Search is a Gmail query, not a sentence

`gmail_search` takes Gmail's own syntax in `query`. Translate what the user asked into it rather than
passing their words through:

| They ask | query |
|---|---|
| what's unread | `is:unread` |
| anything from Anna this week | `from:anna newer_than:7d` |
| the invoice from Stripe | `from:stripe (invoice OR receipt) has:attachment` |
| did the booking arrive | `newer_than:3d (booking OR confirmation OR reservation)` |
| mail I have not answered | `is:unread -category:promotions -category:social` |

Useful operators: `from:` `to:` `subject:` `is:unread` `is:starred` `has:attachment` `label:`
`in:anywhere` (includes spam and trash — normal search does not) `newer_than:7d` `older_than:1y`
`before:2026/08/01`. Combine with a space (AND), `OR`, and `-` to exclude.

Two habits that make the difference:

- **Exclude the noise unless asked.** `-category:promotions -category:social` turns "what's unread"
  from forty newsletters into the five that matter. Say that you did.
- **Bound the time.** Almost every question is about recent mail; `newer_than:14d` keeps the answer
  about now and the result small.

## Reading costs context, so read on purpose

`gmail_search` already returns sender, subject, date and a snippet. That answers "did X arrive" and
"what's unread" on its own — do not open messages to confirm what the snippet already says.

Call `gmail_read` when the user wants the content: what someone actually wrote, a detail inside, a
number to extract. One or two messages, not the whole result. If several look relevant, say which and
ask, rather than reading ten.

## Answering

Lead with the answer, not the mechanics. "Three unread since yesterday: Anna about the lease, a
Stripe receipt, and a newsletter." Then the detail if it was asked for.

Say plainly when nothing matched, and say what you searched for — a query that was too narrow looks
exactly like an empty inbox otherwise.

If a tool reports that the account is not connected, say that mail needs connecting once with
`nocturn auth gmail` on the machine running the daemon, and stop. Do not try other tools to get at
the mail another way.
