# Examples

A workspace with one of everything, so you can see what the folder looks like before you build your
own — and copy it if you would rather start from something that already runs.

```bash
cp -r examples/workspace nocturn-data/workspaces/demo
./nocturn serve
```

Everything in it is inert until you point it at something. The agent needs a model, the plugin needs
the network approved, the MCP server needs an account connected. Nothing here holds a credential.

## What is in it

```
workspace/
  PERSONA.md                      who the assistant is
  agents/morning-briefing/        one cron agent
  skills/summarize-url/           one skill
  plugins/weather/                one sandboxed plugin
  mcp/github/                     one remote MCP server
  memory/people/lina.md           one memory note
  mnt/                            ← the ONLY part the model can read and write as files
    knowledge/                    documents to search
```

The split at `mnt/` is the whole security layout in one line. Everything above it is **control
plane** — who the assistant is, what it may run, what it remembers — and the file tools cannot reach
any of it, because it is simply not inside the mount. Everything below is data.

### `PERSONA.md`

The system prompt. Optional: without it the assistant uses a built-in default.

### `agents/morning-briefing/`

A [cron agent](https://efuturetoday.github.io/nocturn/guides/agents/). The frontmatter is the
interesting part:

```yaml
tools: [http_read, knowledge_search, notify]
when: "0 7 * * *"
autonomy: guarded
```

`tools` is the **cage** — this agent has those three and nothing else, not "has everything but is
denied the rest". `autonomy: guarded` routes an approval to your phone; the default, `strict`, has no
approver at all and refuses instead. A missing or misspelled setting therefore reduces authority
rather than granting it.

Delete the `when:` line and it only fires when you ask.

### `skills/summarize-url/`

A [skill](https://efuturetoday.github.io/nocturn/guides/skills/) is procedural knowledge in Markdown
— how to do something, in words. It grants nothing: the model already had `http_read`, and this only
tells it what a good summary looks like. That is why a self-written skill is not a security problem.

### `plugins/weather/`

A [plugin](https://efuturetoday.github.io/nocturn/guides/writing-plugins/) is JavaScript in the WASM
sandbox. Read `plugin.json` before `plugin.js` — the manifest is the entire authority story:

```json
"uses": ["http_read"]
```

That is the plugin's cage, and you can review it **without running the artifact**. The guest itself
has no filesystem, no sockets and no clock; the only thing it can do is call the tools it declared,
and each of those is gated exactly as it would be for the model.

### `mcp/github/`

A [remote MCP server](https://efuturetoday.github.io/nocturn/guides/remote-mcp/). HTTPS only — a
local stdio server would be a foreign process running with your rights, which is the supply-chain
problem the sandbox exists to avoid.

`"auth": "oauth"` means the daemon discovers the flow and stores the token host-side. Run
`nocturn auth github` once; the model never sees it.

### `memory/people/lina.md`

A [memory note](https://efuturetoday.github.io/nocturn/guides/memory/) — one fact, one file, with a
one-line summary in its frontmatter. Those summaries form the catalog that goes into every prompt;
the bodies are loaded only when asked for.

Note where it lives: **outside** `mnt/`. Text that reaches every future prompt must not be writable
through a generic file tool.

### `mnt/knowledge/`

Documents to search, and the mirror image of memory: inside the mount, never in the prompt, reached
only when [`knowledge_search`](https://efuturetoday.github.io/nocturn/reference/tools/knowledge_search/)
goes looking.

Try it with the lease and the heat-pump manual:

> how much was the deposit and when do I get it back?

> what does E11 mean on the heat pump?

The first is a paraphrase — the document says "three months' base rent", not "3,720 euros" in the
sentence you asked about. The second is an error code no embedding model has ever seen, found by the
exact word. One question for each half of the search.

This needs an embedding endpoint configured, and indexing sends those documents to it. See
[knowledge](https://efuturetoday.github.io/nocturn/guides/knowledge/).
