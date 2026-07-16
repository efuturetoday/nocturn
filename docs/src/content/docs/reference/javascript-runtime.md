---
title: JavaScript runtime (code.run)
description: The sandboxed QuickJS engine behind code.run — what JavaScript you can write, the built-in APIs, and how a script reaches gated effects.
---

`code.run` runs JavaScript on a **QuickJS** interpreter (quickjs-ng, pinned at `v0.10.1`)
compiled to `wasm32-wasi` and executed inside the [sandbox](/reference/wasm-abi/). It is how
the assistant does multi-step computation and data shaping — and, when it needs to, reaches a
real effect through the one gate.

The defining property: **pure computation needs zero authority.** A script that only transforms
data never touches the broker. Every effect it *does* perform is an individual, gated tool call.

## At a glance

|                 |                                                            |
|-----------------|------------------------------------------------------------|
| **Engine**      | QuickJS (quickjs-ng `v0.10.1`), ES2023, `wasm32-wasi`      |
| **Tool**        | `code.run` — input `source` (a JS program)                 |
| **Input**       | your script; a runtime prelude is prepended automatically  |
| **Output**      | whatever the script prints (`console.log` / `print`) → stdout |
| **Effects**     | only via `nocturn.call(tool, args)` — each one gated        |
| **Authority**   | none by default; the sandbox caps memory and wall-clock time |

## How a run works

1. Your `source` is evaluated after the runtime prelude is prepended (so your line numbers are
   offset by the prelude's length — worth knowing when reading a stack trace).
2. Anything you `console.log` or `print` goes to stdout and comes back as the tool result.
3. An **uncaught exception** ends the run: its message and stack go to stderr and the call fails.
4. A runaway script (infinite loop, memory blow-up) is **trapped** by the sandbox's memory cap
   and wall-clock deadline — it cannot hang the host.

```js
// Pure compute — no approval, no capabilities touched.
const rows = [3, 1, 2].sort((a, b) => a - b);
console.log(JSON.stringify(rows));   // → [1,2,3]
```

## Reaching effects: `nocturn.call`

Every real-world effect bottoms out at one host function:

```js
const out = nocturn.call(toolName, args);
```

- It is **synchronous** — it returns the tool's result **string** directly, no `await` needed
  (top-level `await` also works if you prefer promises).
- `toolName` and `args` are the **same tool names and argument schemas** the model sees. Calling
  `nocturn.call` reaches the identical [gated capabilities](/reference/capabilities/) — broker +
  out-of-band approval — so a script has no more authority than the model does.
- A denied or failed effect **throws** a JavaScript exception (the host's `error:` response
  becomes a `throw`), so your script can `try/catch` it and the host never crashes.

```js
// An effect — gated exactly like the model calling http.read.
const res = nocturn.call("http.read", { url: "https://example.com" });
console.log(JSON.parse(res).status);   // → 200
```

`code.run` itself is **not** callable from within a script (no recursive interpreter).

## Built-in APIs (the prelude)

A small hand-written prelude ships a familiar **subset** of Web/Node APIs on top of QuickJS. It
is DevEx sugar, not a security boundary (see below).

### Pure-compute helpers (no authority)

Plain polyfills — they never call the gate:

| API | Notes |
|-----|-------|
| `btoa` / `atob` | base64 over a binary string |
| `TextEncoder` / `TextDecoder` | UTF-8 encode/decode |
| `Buffer.from(...)` | `utf8` / `base64` / `base64url` / `hex` only; `.toString(enc)` back |
| `URL`, `URLSearchParams` | pragmatic `http(s)` parser (not full WHATWG) |
| `Headers`, `Response`, `FormData` | fetch-style value types |
| `require(...)` | shim for `fs`, `fs/promises`, `buffer`, `util` only |
| `console.log`, `print` | write to stdout |

### Effectful sugar (bottoms out at gated tools)

These look like the Web/Node APIs but route through `nocturn.call`, so every effect is gated:

- **`fetch(url, opts)`** → maps onto `http.read` (GET/HEAD) or `http.write` (other methods) and
  resolves a `Response` with `.text()`, `.json()`, `.arrayBuffer()`, `.status`, `.ok`.
  Request headers other than `Content-Type` are **not** forwarded — the host owns the credential
  channel and injects auth at the boundary. A denied/failed request rejects, like a network error.

  ```js
  const r = await fetch("https://api.example.com/items");
  const items = await r.json();
  console.log(items.length);
  ```

- **`fs` / `nocturn.fs`** → the workspace filesystem via the `file.*` capability. `nocturn.fs`
  is promise-based (`readFile`, `writeFile`, `list`, `stat`, `search`, `remove`, `move`); a
  node-ish synchronous shim (`readFileSync`, `writeFileSync`, `readdirSync`, `statSync`,
  `existsSync`, `unlinkSync`, `renameSync`, …) is also provided. Unsupported operations (streams,
  recursive `rm`, `mkdirSync`, …) throw a clear error rather than failing silently.

  ```js
  const fs = require("fs");
  const text = fs.readFileSync("notes/todo.md");   // gated file.read
  fs.writeFileSync("notes/done.md", text);          // gated file.write → asks
  fs.renameSync("notes/done.md", "done/todo.md");   // gated file.move → asks

  const mds = await nocturn.fs.search("*.md");      // gated file.search (read)
  ```

- **`nocturn.ping(host)`** → an ICMP reachability probe via the `ping` capability (a read, like
  `dns.resolve`). Returns `{host, ip, ok, rtt_ms}`.

- **`nocturn.resolve(host, type?)`** → a DNS lookup via the `dns` capability. `type` ∈
  `A`/`AAAA`/`IP`/`MX`/`TXT`/`CNAME`/`NS`/`PTR`/`SRV` (default `A`). Returns `{host, type, records}`.

- **`nocturn.now()`** → the current date/time (`{unix, iso, utc, timezone, offset_seconds}`). The
  sandbox guest has no wall clock of its own, so this is the way to get the time. It carries **no
  authority** and is never gated.

  ```js
  const today = nocturn.now().iso;                  // e.g. "2026-07-17T15:04:05+02:00"
  ```

:::note[Available effects follow the capability set]
`fetch` and `fs` cover the stable HTTP and file capabilities. Because a script reaches effects by
**name** through `nocturn.call`, any capability registered on the host is callable the moment it
exists — the interpreter never changes to add one. See the
[capabilities reference](/reference/capabilities/) for the current, authoritative list of tools,
their inputs, and their read/write axis.
:::

## What it is not

The runtime is a deliberate subset, not a full Node.js or browser:

- **No ambient I/O.** There is no raw socket, no arbitrary filesystem, no child process. The only
  way out is a gated `nocturn.call` (and the `fetch`/`fs` sugar over it).
- **No package ecosystem.** `require` resolves only the four shim modules above; there is no
  `npm`, no module loader.
- **Bounded.** Memory is capped and a wall-clock deadline traps runaways; a single `file.read`
  is capped (see the [Files capability](/reference/files/)).

## Security note

The prelude runs **inside** the sandbox guest with no more authority than any plugin code. A buggy
or malicious shim can do nothing `nocturn.call` does not already allow — it is convenience, not a
trust boundary. The reference monitor is `Guard.Authorize` on the host, unchanged: every effect,
whether written as `fetch(...)`, `fs.writeFileSync(...)`, or a bare `nocturn.call(...)`, passes the
same broker and the same out-of-band approval.
