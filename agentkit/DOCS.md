# agentkit

A small, **zero-dependency** Go library for building AI agents and chat sessions: it drives an
LLM through an agentic tool-calling loop, streams the output, and lets you compose tools, skills,
sub-agents and guards. Everything external is a **port** (interface) you plug an adapter into — the
core imports nothing outside the standard library.

> Module: `github.com/efuturetoday/agentkit`. Single flat package `agentkit`, one file per concept.
> Provider adapters (e.g. OpenAI) live in separate modules so the core stays dependency-free.

---

## Design principles

1. **Zero third-party dependencies in the core.** The whole engine is stdlib-only. Anything that
   would pull a dependency (an LLM SDK, a tokenizer, a YAML parser) is a port with the
   implementation in a separate adapter module.
2. **Ports & adapters.** Every boundary is an interface: `LLM` (the model), `Tool` (an effect),
   `Logger`, `Store`. The core depends on the interface; you supply the adapter.
3. **Policy-blind.** The library has no concept of permissions, approval, or safety policy. A tool
   carries its own gating **inside its `Call`** — the library just dispatches. What a tool is
   allowed to do is entirely the tool author's business.
4. **Immutable tool/skill sets.** `ToolSet` and `SkillSet` are named maps; a subset (`Select`) is a
   new copy, never a mutation. Handing a restricted subset to a sub-agent is a hard bound.
5. **Observability is a one-way event stream.** The loop emits raw, time-ordered events; a UI
   renders them and any aggregation (token totals, latency, cost) is built by the consumer.
6. **Name things what they are.** No ambiguous names described by a comment — `TokenCount` not
   "usage", `WithTimeout` not "budget", `ToolSpec` not "spec".

---

## The pieces

| Type / func | Role |
| --- | --- |
| `LLM` | The model port: `Next(ctx, conv, tools) (Step, error)`. An adapter streams token/reasoning deltas to the ctx event sink and fills `Step.Tokens`. |
| `Session` | The conversation unit. Serialized turn loop, `Submit(input)` in / `Subscribe() <-chan Event` out; holds history. Built with `NewSession(ctx, llm, opts...)` — ctx is its lifecycle; `Close()` (or cancelling ctx) stops the loop, aborts any in-flight turn, and closes the stream. |
| `Once(ctx, llm, input, opts...)` | Synchronous one-shot: run a throwaway session to its final answer. The primitive under agent firing and sub-agents. |
| `Tool` | The effect port: `Spec() ToolSpec` + `Call(ctx, args) (string, error)`. Args are raw JSON; the tool validates them itself. Any gating lives in `Call`. |
| `NewTool(name, description, fn, opts...)` | Build a tool from a closure. Options: `WithSchema(json)`, `WithMaxChars(n)`. Returns an error if the spec is invalid. |
| `ToolSet` | `map[string]Tool` (named map): `Select`, `Specs`, `Call(ctx, name, args)`. Immutable by convention. |
| `Skill` / `SkillSet` | Context, zero authority. Immutable set with `Select`, `Specs` (catalog) and `LoadTool()` (the progressive-disclosure `skill.load` tool). |
| `Agent` | A declaration: name, instructions, a tool filter, effort. No authority, no schedule. |
| `AgentTool(a, llm, tools, opts...)` | Expose an agent as a callable `Tool` — a sub-agent is just a tool. |
| `Message` / `ToolCall` / `Step` / `TokenCount` | The conversation model and one model round-trip's output + token count. |
| `Event` and variants | The output stream (see below). |
| `Logger` | Leveled key/value port with `WithContext`. `NopLogger()` default, `SlogLogger(*slog.Logger)` adapter. |
| `Store` / `MemStore` | Transcript persistence port + in-memory default. Attached per session with `WithStore(store, id)`. |
| `Diagnostics` | A passive collector fed advisory findings from across the pipeline. |

---

## Turn loop

A **turn** is one user input driven to a final answer. Internally the loop repeats: ask the `LLM`
for the next `Step`; if it returns tool calls, run them **in parallel** through the `ToolSet` and
feed the results back; stop when the model returns a final answer or a guard trips.

Tool results are matched to their calls by **native id** (`ToolCall.ID` / `Message.ToolCallID`),
never positionally — this is what makes parallel tool execution safe.

---

## Events and the frame model

`Subscribe()` yields a channel of `Event`. The set is a sealed interface (unexported marker), so a
consumer type-switches exhaustively:

```go
for ev := range sess.Subscribe() {
    switch e := ev.(type) {
    case agentkit.Token:     ui.append(e.Frame, e.Text)
    case agentkit.Thinking:  ui.reason(e.Frame, e.Text)
    case agentkit.ToolStart: ui.openTool(e.Frame, e.ID, e.Tool)
    case agentkit.ToolEnd:   ui.closeTool(e.ID, e.Result, e.Duration)
    case agentkit.TurnEnd:   ui.total(e.Frame, e.Tokens)
    }
}
```

Every event carries **`Frame`**: the id of the enclosing call. `Frame == 0` is the top-level
(main) agent. A tool call opens a new frame whose id is its `ToolStart.ID`; everything emitted
inside that call carries it. Because a **sub-agent runs inside an `AgentTool` call**, all of its
events (its tokens, its own tool calls, its `TurnEnd`) carry that call's id as `Frame`.

This makes the main and sub-agent streams **fully differentiable**:
- group events by `Frame`;
- render each non-zero `Frame` as a nested, collapsible/**hideable** card under its tool call;
- nest to any depth — a sub-agent's sub-agent gets its own frame.

`Emit` stamps `Frame` from ctx; emitters (adapters, tools) never set it.

### Call-instance ids

Ids are **call-instance** identity, not tool identity (the name is that). The same tool called
twice in a turn, or a nested call, each gets a fresh id from a counter carried in ctx. The
`Frame`/`ID` pairing forms the parent/child forest a UI renders. Nesting works because ctx flows
into every `Call`.

---

## Sub-agents

A **sub-agent is a tool.** `AgentTool(a, llm, tools)` wraps an `Agent` as a `Tool` whose `Call`
runs the agent to a final answer (`Once`) over the input argument, with the agent's instructions as
system prompt and `tools.Select(a.Matches)` as its toolset. Add it to a parent's `ToolSet` and the
parent can delegate to it like any tool. Nesting, event differentiation and budget inheritance all
follow from ctx propagation — no separate sub-agent subsystem exists.

---

## Guards (stop conditions)

Three independent per-turn stop dimensions, named unambiguously:

```go
WithMaxSteps(n)    // model round-trips
WithTimeout(d)     // wall-clock (pausable — see below)
WithTokenLimit(n)  // cumulative billed tokens
```

A turn ends on whichever trips first; the reason is on `TurnEnd.Err` (`ErrMaxSteps`,
`ErrTokenLimit`, or a context deadline).

- **Wall-clock is pausable:** time spent waiting on a blocking out-of-band step does not count, so a
  human deciding never trips the timeout.
- **Token limit is reactive:** the loop sums each round-trip's `Step.Tokens` and stops before the
  next round-trip once the running total reaches the limit. The count comes from the provider
  response — **no tokenizer needed**.

### Budget inheritance across sub-agents

Time and token spend are **depletable, global** resources carried in ctx and **shared across
nesting**. A session installs its own budget into ctx **only when none is present** (it is the
top-level run); an embedded run (a sub-agent) **inherits** the outer session's remaining pool. So a
parent's `WithTimeout` / `WithTokenLimit` cap the parent **and all its sub-agents together**; a
sub-agent's own budget applies only when it runs standalone.

`maxSteps` is the exception — a per-run round-trip valve, counted fresh by each loop, never
inherited (a step count is not a depletable pool).

### Guarding nested sub-agent spawns

A sub-agent tree is bounded on four axes — because a depth cap alone is not enough (a depth-capped
tree can still fan out to a huge descendant count and pin CPU):

1. **Shared tree-global budget (primary).** The inherited token/time pool above caps the entire
   tree's cost and wall-clock regardless of depth or breadth. This is the strongest guard and it
   propagates automatically through ctx.
2. **Depth cap — `WithMaxDepth(n)`.** A per-branch counter; a spawn past it is refused with
   `ErrMaxDepth` (returned to the model as the tool result, so it finishes directly).
3. **Population cap — `WithMaxSpawns(n)`.** A counter shared across the whole tree, capping the
   total number of sub-agents — the runaway-fan-out guard depth cannot provide.
4. **Per-agent tool allowlist.** An agent can only spawn the `AgentTool`s present in its `ToolSet`.
   Omit them (or don't include an agent's tool in another's set) and it is a leaf — this both makes
   a node non-spawning and prevents A→B→A cycles by construction.

Depth and population are set at the top level and **inherited** by nested runs (like the budget), so
a sub-agent cannot reset them. `maxSteps` still bounds each individual agent's own loop.

---

## Token accounting

`TokenCount{Prompt, Completion, Total}`. `Step.Tokens` is one round-trip (the adapter fills it from
the provider response). `TurnEnd.Tokens` is the turn total — the **sum** of every round-trip, which
is what you are **billed** (history is re-sent each call). To gauge context-window fill instead,
read the **last** round-trip's `Prompt`, not the sum. Session-level totals are the consumer's job,
built by draining `TurnEnd` events. A `Tokenizer` port would only be needed for **proactive**
pre-send estimation (an optional adapter), never for reporting.

---

## Validation vs diagnostics

Two separate mechanisms:

- **`Validate()` — hard.** `ToolSpec.Validate` / `Skill.Validate` enforce only the rules a provider
  rejects outright: tool name `^[a-zA-Z0-9_-]{1,64}$`, skill name `^[a-z0-9-]{1,64}$` (lowercase,
  no underscore, no reserved words), well-formed parameter JSON. Enforced at construction —
  `NewTool` / `NewToolSet` / `NewSkillSet` return an error, so an invalid tool or skill never
  exists.
- **`Diagnostics` — soft.** A passive collector fed advisory findings (`Warn`/`Info`) from various
  corners — e.g. a description longer than 1024 chars (rejected by some providers, tolerated by
  others). Non-fatal; the consumer drains `All()` / checks `HasErrors()` and logs.

**JSON Schema is pass-through.** A tool's `Parameters` is raw JSON forwarded to the provider to
constrain generation; the library only checks that it is well-formed JSON (no schema-validator
dependency). Validating call **arguments** against the schema is the **tool's** job inside `Call`
(unmarshal, check, return the error to the model, let it retry).

---

## Persistence and multiplexing

A `Session` is self-contained. `WithStore(store, id)` loads its history on build and saves it after
each turn; `MemStore` is the in-memory default and any durable `Store` is a consumer adapter.

Running **many** concurrent conversations (**multiplexing**) is the consumer's job: keep a
`map[id]*Session`, create on first message, route input to the right session and fan its
`Subscribe()` stream out to that conversation's UI. Eviction and lifecycle are app policy, so the
library owns no live-session manager. (Contrast: multiplexing is *horizontal* — independent
top-level conversations the app routes; sub-agents are *vertical* — nested inside one conversation,
handled by the library via ctx.)

---

## Logging

`Logger` is a small leveled key/value port (`Debug/Info/Warn/Error`, plus `WithContext(ctx)` for
request/trace scoping). Plug in anything; `NopLogger()` is the silent default and `SlogLogger`
wraps the standard library's `slog`. It is never a hard-typed `*slog.Logger`, so it stays
dependency-free.

---

## Adapters

The core defines ports; adapters implement them in their own modules so their dependencies never
reach the core:

- **`agentkit-openai`** — implements `LLM` against an OpenAI-compatible endpoint: streaming SSE,
  native tool calls, reasoning deltas, `Step.Tokens` from the response usage. Options include
  `WithMaxTokens(n)` (the per-response **output** cap — distinct from the session's
  `WithTokenLimit`, which is cumulative spend).
- A **tokenizer** adapter (optional) would implement a `Tokenizer` port for proactive pre-send
  token estimation.

---

## Minimal usage

```go
weather, _ := agentkit.NewTool(
    "get_weather", "Current weather for a city.",
    func(ctx context.Context, args string) (string, error) { /* ... */ return "sunny", nil },
    agentkit.WithSchema(json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)),
)
tools, _ := agentkit.NewToolSet(weather)

sess := agentkit.NewSession(ctx, llm, // ctx is the session lifecycle; cancel it or Close() to stop
    agentkit.WithSystem("You are a helpful assistant."),
    agentkit.WithTools(tools),
    agentkit.WithTimeout(30*time.Second),
    agentkit.WithTokenLimit(100_000),
)
defer sess.Close()

go func() {
    for ev := range sess.Subscribe() { /* render by Frame; closes when the session stops */ }
}()
sess.Submit("What's the weather in Cologne?")
```

Or a one-shot:

```go
answer, err := agentkit.Once(ctx, llm, "Summarize this.", agentkit.WithTools(tools))
```
