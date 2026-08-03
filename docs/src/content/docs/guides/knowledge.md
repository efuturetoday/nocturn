---
title: Knowledge
description: A folder you drop documents into, searchable by meaning and by exact words — and what it costs, since indexing sends them to an embedding provider.
---

Put documents in a folder. Ask about them in conversation. That is the whole feature.

```
nocturn-data/workspaces/main/mnt/knowledge/
  contracts/flat-lease.md
  manuals/heat-pump.txt
  meetings/2026-07-14.md
```

The assistant reaches them through one tool,
[`knowledge_search`](/nocturn/reference/tools/knowledge_search/), and only when a question calls for
it. Nothing from this folder is in the prompt otherwise, so a large corpus costs nothing until it is
used.

## Not the same thing as memory

Both store text, and they are opposites in every way that matters.

| | [Memory](/nocturn/guides/memory/) | Knowledge |
|---|---|---|
| What it holds | what the assistant chose to remember about you | what you filed |
| Where | outside the mount — no file tool can reach it | **inside** the mount, at `mnt/knowledge/` |
| Size | capped at a 2 KiB catalog | as large as you like |
| In the prompt | the catalog, every single turn | never — only on demand |
| Written by | `memory_write`, gated | you, or any file tool |

Memory is identity. Knowledge is a library. Memory sits outside the mount because text that reaches
every future prompt must not be writable through a generic file tool; knowledge sits inside it
because documents are data, and putting one there grants nobody anything.

The **index** does not live in the mount. It holds hashes, offsets and vectors — host state — and a
model that could edit it could point a search result at text that is not in the file.

## Setting it up

Retrieval needs an embedding endpoint. If your chat provider serves `/v1/embeddings`, you already
have one:

```ini
# Optional — falls back to OPENAI_BASE_URL and OPENAI_API_KEY
NOCTURN_EMBED_BASE_URL=https://your-embeddings-endpoint.example
NOCTURN_EMBED_API_KEY=your-key-here
NOCTURN_EMBED_MODEL=gemini-embedding-001
NOCTURN_EMBED_DIMS=768
```

The endpoint and key fall back to the chat ones because one gateway usually serves both. **The model
deliberately does not.** `OPENAI_MODEL` names a chat model, and a chat model id handed to
`/v1/embeddings` gets "unknown embedding model" at best, and something that answers meaninglessly at
worst.

Without any of this configured, the tool is **not registered at all** — not registered and failing.

:::danger[Changing the model or dimensions invalidates the index]
A vector only means something relative to the model that produced it. Two different lengths cannot
be compared, which fails loudly. Two different models at the *same* length is the dangerous case:
the arithmetic still works and returns confident nonsense.

The index records both and refuses to answer on a mismatch, naming what to do. After changing
either value:

```bash
rm nocturn-data/workspaces/<name>/knowledge.idx.json
```

The next reconcile rebuilds it — which re-embeds every document, at whatever your provider charges.
:::

### Pin a model before you index anything you care about

`auto` is convenient to try and wrong to build an index with: it is a name whose meaning can change,
and the day the gateway routes it elsewhere the whole index quietly stops meaning anything.

## What is indexed

**Today:** `.md`, `.markdown`, `.mdx`, `.txt`, `.text`.

Anything else is **skipped and named**, not silently ignored — otherwise you drop a PDF in the
folder and never learn it is not searchable. PDFs, Office files and images need an extraction step
that does not exist yet; images in particular need a model to describe them before there is anything
to embed. The code has a port for exactly this, so each is an addition rather than a rewrite.

Code files are deliberately out. Splitting Go or JSON at headings is meaningless, a source tree
produces thousands of poor passages and a real bill, and `file_search` plus `file_read` already find
code without embeddings.

## It keeps itself in step

A running daemon reconciles the folder with the index **every minute**. Add a document and it becomes
searchable; edit one and it is re-indexed; delete one and it leaves. No restart, no command.

That is affordable because a reconcile over an unchanged folder is a directory walk: a file whose
size *and* timestamp both match is never opened, and nothing is written. Only a file whose timestamp
moved is read and hashed — and if the bytes turn out to be identical, it is still not re-embedded.
You pay the provider for changed content, not for looking.

## What this costs you in privacy

Say it plainly: **indexing sends your documents to the embedding provider.** Every file in the
folder, once, and every query when you ask it. If that is not acceptable for a document, it does not
belong in this folder.

Two things reduce the blast radius, and neither removes that fact:

- The **vault leak scanner runs before the request**. A passage carrying a secret you have stored is
  refused rather than embedded, so a credential that got pasted into a document does not travel.
- **Results are redacted** on the way back into the conversation, the same treatment `memory_read`
  gives its own notes.

A local embedding model would remove the trade entirely, and is not built. The honest position today
is a port with a remote adapter behind it.

## How the search actually works

Two rankers, because they fail in opposite directions and running one alone means accepting its
blind spot:

- **Vectors** find a passage that says the same thing in other words — and miss an exact identifier
  the model never saw during training.
- **Keywords** (BM25) find precisely that identifier — and miss every paraphrase.

Their results are fused by **rank**, not by score. A cosine of 0.82 and a BM25 of 11.4 share no
scale, and normalising them would invent one; using only the order each ranker produced means
neither half can dominate by having larger numbers. What wins is agreement.

Documents are split at headings, because a heading is where the document itself says the subject
changed, and a passage spanning two of them answers neither. Each passage carries its heading
breadcrumb into the embedding, so "leave this unset in production" is not orphaned from the section
that says what "this" is — and a result shows you that breadcrumb, so you can check the source.

## Treat what comes back as untrusted

A search result is quoted file content, and the text handed to the model says so. It deliberately
does **not** claim you wrote it.

That is not pedantry. The corpus is inside the mount, so `file_write` can put a document there — and
so, therefore, can a [prompt injection](/nocturn/architecture/threat-model/). Introducing that back
to the model as "your user's own note" would launder exactly the attack everything else here exists
to stop.
