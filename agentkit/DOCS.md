# agentkit — ecosystem design

A small family of Go modules for building AI agents and chat sessions: a zero-dependency engine, a
provider adapter, a permission layer, ready effect tools, and a composition root. This document is
the **cross-cutting design and mental model** — the stuff that spans modules. For the reference of
any type or function, read the package's doc comments: **`go doc ./<module>`** (they are the source
of truth and never drift).

---

## The modules

```
agentkit          engine (zero third-party deps): Session/Once, the LLM & Tool & Store & Logger
                  ports, ToolSet/SkillSet, Agent/AgentTool, the Event stream, turn guards, Schema
agentkit-openai   LLM adapter over go-openai: streaming SSE, native tool calls, usage
agentkit-gate     permission layer: Policy/Ruling/Recall, Grant/Matcher, Approver, Check/Wrap
agentkit-tools    ready-made effect tools that gate their own effect (http_get + the host axis)
agentkit-runtime  composition root: New(llm, WithTools/WithGate/WithSession) → ready Sessions
```

Dependency direction (each depends only on what it must):

```
runtime ─┬─▶ agentkit          agentkit → stdlib only
         └─▶ gate ──▶ agentkit  openai   → agentkit (+ go-openai)
tools ─┬─▶ agentkit             gate     → agentkit
       └─▶ gate
```

The engine stays dependency-free by pushing every external thing behind a **port** (interface) and
every provider/effect into a **separate module**. A consumer picks the modules it needs; `runtime`
wires them.

---

## Design principles

1. **Zero third-party deps in the engine.** Anything that would pull a dependency (an LLM SDK, a
   tokenizer, a YAML parser, a permission model) is a port with the implementation in another module.
2. **Ports & adapters.** Every boundary is an interface — `LLM`, `Tool`, `Logger`, `Store`,
   `gate.Policy`, `gate.Approver`. The core depends on the interface; you supply the adapter.
3. **The engine is policy-blind.** It has no notion of permission or safety. A tool carries its own
   gating **inside `Call`**; `agentkit-gate` is one way to add that, layered on top — never baked in.
4. **Name things what they are.** No ambiguous name explained by a comment (`TokenCount` not "usage",
   `WithTimeout` not "budget"). Opaque types with constructors where a zero value would be unsafe.
5. **Fail closed.** A forgotten field must never silently mean allow-all / permanent / any-host.
   Zero values are the safest option (`gate.RecallNever`, deny-by-default).

---

## The two axes of control

Two orthogonal questions, two mechanisms — keep them separate:

- **Cage — which tools an agent HAS at all.** This is `agentkit.ToolSet` + `Select`: a static,
  set-once bound. A tool outside the set is *unreachable* (the model never sees it). Bounded, known,
  chosen up front (a checklist / template), never asked at runtime.
- **Gate — what a tool may DO.** This is `agentkit-gate`: a per-action allow / ask / deny, with a
  human prompted out-of-band on the risky ones and the answer remembered. For runtime, unbounded,
  discovered targets (which host, which path) — deny-by-default, ask-on-new, remember.

Hosts are *unbounded* (discovered at runtime) → deny + ask-on-new. Tools are a *bounded* known set →
pick up front. That difference is why they are different mechanisms.

---

## The turn loop and the frame model

A **turn** drives one user input to a final answer: ask the model for the next step; while it returns
tool calls, run them **in parallel** through the tool set and feed the results back; stop on a final
answer or when a guard trips. Results match their calls by **native id**, never positionally — this
is what makes parallel execution safe.

The output is a one-way **event stream** (`Subscribe()`), a sealed union the consumer type-switches
over. Every event carries a **`Frame`**: the id of the enclosing call (`0` = the top-level agent). A
tool call opens a frame whose id is its `ToolStart.ID`; everything emitted inside carries it. Because
a **sub-agent runs inside an `AgentTool` call**, all of its events carry that call's id as `Frame` —
so the main and sub-agent streams are **fully differentiable**: group by `Frame`, render each
non-zero frame as a nested (collapsible/hideable) card, nest to any depth. Ids are call-instance
identity (a counter in ctx), not tool identity (the name is that) — the same tool called twice, or
nested, each gets a fresh id. Nesting works because ctx flows into every `Call`.

---

## Guards and budget inheritance

Every turn is bounded on independent stop dimensions — round-trips (`WithMaxSteps`), pausable
wall-clock (`WithTimeout`; time spent waiting on a human doesn't count), and cumulative billed tokens
(`WithTokenLimit`, from the provider's usage — no tokenizer). A turn ends on whichever trips first.

Time and token spend are **depletable, global** resources carried in ctx and **shared across
nesting**: a session installs its budget only if none is present, so a sub-agent **inherits** the
outer pool. A parent's limits therefore cap the parent *and all its sub-agents together*; a
sub-agent's own budget applies only standalone. `maxSteps` is the exception — a per-run valve, never
inherited (a step count is not a depletable pool).

A sub-agent **tree** is bounded on four axes, because a depth cap alone lets a tree fan out wide:
(1) the shared tree-global budget (primary — caps total cost regardless of shape); (2) `WithMaxDepth`
(refused with `ErrMaxDepth`); (3) `WithMaxSpawns` (a shared population counter — the fan-out guard
depth can't provide); (4) the per-agent toolset (an agent can only spawn the `AgentTool`s in its set;
omit them and it's a leaf, which also prevents A→B→A cycles). Depth and population inherit like the
budget, so a sub-agent can't reset them.

---

## Sub-agents

A **sub-agent is just a tool.** `AgentTool` wraps an `Agent` (name + instructions + effort) as a
`Tool` whose `Call` runs the agent to a final answer over the input, scoped by the `ToolSet` the
caller passes (`nil` = leaf, `Select(keep)` = subset). Nesting, event differentiation, budget
inheritance and spawn guards all fall out of ctx propagation — there is no separate sub-agent
subsystem. Contrast **multiplexing** (many independent top-level conversations) which is *horizontal*
and the consumer's job (a `map[id]*Session`, its own eviction policy) with sub-agents which are
*vertical* and the library's.

---

## Schema portability

A tool's parameter schema is a **canonical `*Schema`**, not raw JSON — built with `Object`/`Prop`/
`String`/… or mapped from a foreign JSON Schema with `ParseSchema`. The model holds only the
**portable subset every provider accepts** (type, description, properties, required, items, enum, and
nesting — the OpenAI ∩ Anthropic ∩ Gemini intersection). Each adapter **renders** it to that
provider's dialect (e.g. Gemini uppercases types and forbids `additionalProperties`). Because the
model can't even express an unsupported construct, there is **nothing to strip** — no blocklist, no
sanitizer. Validating call *arguments* against the schema stays the tool's own job inside `Call`.

---

## The permission model (agentkit-gate)

The engine is policy-blind; `agentkit-gate` adds human-approved, remembered permission by wrapping
tools and reading a `ctx`-installed policy. The shape:

- An **`Action{Kind, Target}`** is what's gated — `Kind` is a tool name *or* a shared axis like
  `"net"` (so one host allowlist covers every network tool); `Target` is the runtime host/path.
- A **`Policy`** returns a `Ruling` — `Allowed()`, `Denied()`, or `AskWith(recall)`. The `Ruling` is
  opaque and built only through those constructors, so an ask can never silently default its recall.
- **`Recall`** (`Never` < `Session` < `Always`, zero = `Never`) caps, per Kind, how long an approval
  may be remembered. An irreversible Kind uses `RecallNever` → asked every time. `Check` remembers at
  the **more restrictive** of the policy's cap and the human's choice (`min`).
- A **`Grant{Kind, Target}`** is a remembered approval — covering both the tool-action axis and a
  host allowlist. Whether a stored pattern covers an action is decided by a **`Matcher`** the *tool*
  passes to `Check` (`"*.example.com"` over subdomains for hosts, a glob for paths). Target semantics
  live in the tool, never in the general library.
- An **`Approver`** presents the action out-of-band, receives the tool's **suggested widenings**, and
  returns approve/deny plus the grant to remember and its recall. A nil Approver = unattended = deny.

**Roles:** the tool author picks which axes a tool checks and supplies the matcher + suggestions; the
consumer's policy classifies each Kind (allow/ask/deny + recall — this is where "reads free, writes
ask" lives); the human approves once/always/widened; the engine's `ToolSet` is the cage. Supervision
scales it: an attended agent runs loose with HITL; an unattended one is caged tight (no human to ask).

---

## Composition

```go
llm := openai.New(baseURL, apiKey, model)
tools, _ := agentkit.NewToolSet(myTools...)          // the cage
policy := gate.Classify([]string{"notify", "net"}, nil)

rt := runtime.New(llm,
    runtime.WithTools(tools),
    runtime.WithGate(policy, gate.NewMemGrants(), myApprover),
    runtime.WithSession(agentkit.WithSystem(...), agentkit.WithTimeout(2*time.Minute)),
)
sess := rt.Session(ctx)   // gated tools + permission machinery on the ctx
```

`runtime` wraps the toolset with the gate and installs the permission machinery on each session's
ctx, so it reaches every tool call and every sub-agent. What a consumer still brings: a durable
`gate.Grants` store, a real out-of-band `gate.Approver` (push/ntfy), and its own effect tools beyond
`agentkit-tools`.
