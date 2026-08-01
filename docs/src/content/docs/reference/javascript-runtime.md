---
title: JavaScript runtime (code_run)
description: The sandboxed QuickJS engine behind code_run — what you can write, the built-in APIs, and how a script calls a gated tool.
---

`code_run` runs JavaScript on a **QuickJS** interpreter (quickjs-ng, pinned at `v0.10.1`) compiled
to `wasm32-wasi` and executed inside the [sandbox](/nocturn/reference/wasm-abi/). It is how the assistant
does multi-step computation and data shaping — and, when it needs to, calls a tool through one host
function.

The defining property: **pure computation needs zero authority.** A script that only transforms data
never touches the gate. Everything it *does* to the world is an individual tool call, gated where
that tool is gated.

## At a glance

| | |
|---|---|
| **Engine** | QuickJS (quickjs-ng `v0.10.1`), ES2023, `wasm32-wasi` |
| **Tool** | [`code_run`](/nocturn/reference/tools/code_run/) — input `source` (a JS program) |
| **Input** | your script; a runtime prelude is prepended automatically |
| **Output** | whatever the script prints (`console.log` / `print`) → stdout |
| **Tool calls** | only via `nocturn.call(tool, args)` — each gated where that tool is gated |
| **Authority** | none by default; the sandbox caps memory and wall-clock time |

## How a run works

1. Your `source` is evaluated after the runtime prelude is prepended (so your line numbers are
   offset by the prelude's length — worth knowing when reading a stack trace).
2. Anything you `console.log` or `print` goes to stdout and comes back as the tool result.
3. An **uncaught exception** ends the run: its message and stack go to stderr and the call fails
   with that text, so a failure is never a silent empty string.
4. A runaway script (infinite loop, memory blow-up) is **trapped** by the sandbox's memory cap and
   wall-clock deadline — it cannot hang the host.

```js
// Pure compute — nothing gated, nothing asked.
const rows = [3, 1, 2].sort((a, b) => a - b);
console.log(JSON.stringify(rows));   // → [1,2,3]
```

## Calling tools: `nocturn.call`

Every tool call bottoms out at one host function:

```js
const out = nocturn.call(toolName, args);
```

- It is **synchronous** — it returns the tool's result **string** directly, no `await` needed
  (top-level `await` also works if you prefer promises).
- `toolName` and `args` are the **same names and schemas the model uses** — underscores and all.
  The script dispatches through the *same* toolset the model does, so it has no more authority than
  the model, and each call is gated where that tool is gated.
- A denied or failed call **throws**, so your script can `try/catch` it and the host never crashes.

```js
// A tool call — gated exactly like the model calling http_read: net → example.com.
const res = nocturn.call("http_read", { url: "https://example.com" });
console.log(JSON.parse(res).status);   // → 200
```

`code_run` itself is **not** callable from within a script: there is no recursive interpreter, and
the attempt is refused with a catchable error.

## Built-in APIs (the prelude)

A small hand-written prelude ships a familiar **subset** of Web/Node APIs on top of QuickJS. It is
DevEx sugar, not a security boundary (see below).

### Pure-compute helpers (no authority)

| API | Notes |
|---|---|
| `btoa` / `atob` | base64 over a binary string |
| `TextEncoder` / `TextDecoder` | UTF-8 encode/decode |
| `Buffer.from(...)` | `utf8` / `base64` / `base64url` / `hex` only; `.toString(enc)` back |
| `URL`, `URLSearchParams` | pragmatic `http(s)` parser (not full WHATWG) |
| `Headers`, `Response`, `FormData` | fetch-style value types |
| `require(...)` | shim for `fs`, `fs/promises`, `buffer`, `util` only |
| `console.log`, `print` | write to stdout |

### Sugar over the tools

Each of these maps to exactly one registered tool. The mapping is the contract — if the tool asks,
the wrapper asks:

| Wrapper | Tool it calls | Gated? |
|---|---|---|
| `fetch(url)` / `fetch(url, {method:"POST",…})` | [`http_read`](/nocturn/reference/tools/http_read/) / [`http_write`](/nocturn/reference/tools/http_write/) | yes, on the host |
| `nocturn.resolve(host, type?)` | [`dns_resolve`](/nocturn/reference/tools/dns_resolve/) | yes, on the host |
| `nocturn.ping(host)` | [`ping`](/nocturn/reference/tools/ping/) | yes, on the host |
| `nocturn.fs.readFile` / `list` / `stat` / `search` | `file_read` / `file_list` / `file_stat` / `file_search` | no |
| `nocturn.fs.writeFile` / `remove` / `move` | `file_write` / `file_remove` / `file_move` | yes, on the path |
| `nocturn.notify(message, title?)` | [`notify`](/nocturn/reference/tools/notify/) | checked, allowed today |
| `nocturn.remind(when, message, title?)` | [`remind`](/nocturn/reference/tools/remind/) | checked, allowed today |
| `nocturn.wake(seconds, note)` | [`wake`](/nocturn/reference/tools/wake/) | no |
| `nocturn.now()` | [`time_now`](/nocturn/reference/tools/time_now/) | no |
| `nocturn.skillFile(skill, path)` | [`skill_read`](/nocturn/reference/tools/skill_read/) | no |

```js
const r = await fetch("https://api.example.com/items");
const items = await r.json();

const fs = require("fs");
const text = fs.readFileSync("notes/todo.md");     // file_read — ungated
fs.writeFileSync("notes/done.md", text);           // file_write — asks
fs.renameSync("notes/done.md", "done/todo.md");    // file_move — asks on the destination

const mds = await nocturn.fs.search("*.md");       // file_search — ungated
const today = nocturn.now().iso;                   // time_now — no authority
```

`fetch` forwards no request headers other than `Content-Type` — the host owns the credential channel
and injects auth at the boundary. Unsupported Node operations (streams, recursive `rm`, `mkdirSync`)
throw a clear error rather than failing quietly.

:::note[The wrappers are a convenience, not the list]
A script calls tools **by name**, so any tool registered on the host is callable the moment it
exists — the interpreter never changes to add one. `remind_list` and `remind_cancel`, for instance,
have no wrapper and are called with `nocturn.call` directly. The
[gate reference](/nocturn/reference/gate/) is the authoritative list.
:::

## Running a skill's script

A skill can bundle a script next to its `SKILL.md`, which makes
[`skill_read`](/nocturn/reference/tools/skill_read/) the other half of `code_run`: the skill's instructions
name a file, the file is fetched as text, and the text is the program.

```js
const src = nocturn.skillFile("summarize-url", "scripts/extract.js");
// hand `src` back as the source of a code_run call, or eval it in place:
const extract = new Function("html", src);
```

`skill_read` is ungated and confined by `os.Root` to that one skill's directory, so it is a way to
ship code with a skill — not a way to read the workspace. Its companion `skill_load` pulls a skill's
instructions into the **model's** context and is deliberately out of a script's reach: a script has
no context to load into.

## What it is not

- **No ambient I/O.** No raw socket, no arbitrary filesystem, no child process. The only way out is
  `nocturn.call` (and the sugar over it).
- **No package ecosystem.** `require` resolves the four shim modules and nothing else. No npm, no
  module loader.
- **Bounded.** Memory is capped and a wall-clock deadline traps runaways; a single `file_read` is
  capped at 1 MiB (see the [`file` kind](/nocturn/reference/gate/file/)).

## Security note

The prelude runs **inside** the guest with no more authority than any plugin code. A buggy or
malicious shim can do nothing `nocturn.call` does not already allow — it is convenience, not a trust
boundary. The check lives on the host, in `gate.Check`, and every tool call passes it whether it was
written as `fetch(...)`, `fs.writeFileSync(...)`, or a bare `nocturn.call(...)`.

A script's reach is its caller's cage. Run from the chat, it dispatches through the workspace's
tools; run inside a plugin, through exactly the base tools that plugin's `uses` list names.
