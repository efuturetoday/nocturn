# agentkit — TODO / deferred

Intentionally out of the first version. None block v1; each has a clear home when needed.

## Multi-modal messages
`Message.Content` is a plain `string` today — text only. Images / audio / files would need a richer
content model (e.g. a slice of typed parts: text, image-url, image-bytes). Touches `Message`, the
`LLM` port, and every adapter's `buildMessages`. Defer until a concrete image use-case exists; keep
the text path simple until then.

## Structured / constrained output
Forcing the model to return JSON matching a schema (OpenAI `response_format` / JSON mode, or a
grammar). Could be a per-turn option carried on ctx and honored by the adapter, or modeled as a
single tool the model must call. Solvable at the edges without a core type — add only when a caller
needs guaranteed-shape output.

## Retries on transient LLM errors
Rate-limit / network / 5xx retries with backoff. This is an **adapter** concern (or a decorator
`func(next LLM) LLM`), NOT a core type — the core stays provider-agnostic. Implement inside an
adapter or as a small reusable `LLM` wrapper; must honor `ctx.Err()` between attempts.

## Accurate tokenizer adapter
The `Tokenizer` port and a rough dependency-free `ApproxTokenizer` (~4 chars/token) exist: the
session estimates a round-trip's tokens at the model boundary when the provider omits usage. An
ACCURATE, model-specific tokenizer (e.g. tiktoken BPE) is still open — it belongs in a SEPARATE
adapter module (it pulls a dependency + data), plugged in via `WithTokenizer`. The same port also
covers proactive pre-send / context-fit estimation.

## Maybe later
- Optional ready-made middleware wrappers (retry/cache/rate-limit) as a SEPARATE module, never core.
