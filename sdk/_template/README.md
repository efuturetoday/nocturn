# Nocturn plugin template

A Nocturn plugin is a sandboxed replacement for an MCP server: a small JS program
plus a `plugin.json` manifest. Its tools show up to the assistant as
`<plugin>.<tool>`. Every effect the code performs (`fetch`, `fs`) is brokered and
gated by out-of-band human approval — a plugin can only reach the hosts and paths
its manifest **cage** declares.

## The runtime you get

The guest runs on an embedded QuickJS interpreter with a small prelude
(`../nocturn.d.ts` documents it). Available out of the box:

- **`fetch`** (+ `Response`, `Headers`, `FormData`, `URL`, `URLSearchParams`) —
  GET/HEAD go through `http.read`, other methods through `http.write`. Bodies may
  be a string, an object (→ JSON), `URLSearchParams` (→ urlencoded), or `FormData`
  (→ multipart). Note: request headers other than `Content-Type` are **not**
  forwarded — Nocturn owns the credential channel and injects auth at the boundary.
- **`fs`** (node-ish, sync + `fs.promises`) and **`nocturn.fs`** (clean async) —
  confined to the workspace. `readFile`/`writeFile`/`readdir`/`stat`/`exists`/
  `unlink` are backed by the gated `file.*` tools; `mkdir`/streams/etc. throw.
- **`btoa`/`atob`, `TextEncoder`/`TextDecoder`, `Buffer`** (utf8/base64/base64url/hex).
- **`nocturn.call(tool, args)`** — the raw gate, for tools without a shim (e.g.
  `dns.resolve`). Synchronous; a denied effect throws.

## Authoring options

### 1. Plain JS + JSDoc (default, no build)

Edit `plugin.js`. `// @ts-check` + `tsconfig.json` give you full type-checking and
autocomplete against `nocturn.d.ts`, and the file runs as-is — no transpile step.

### 2. TypeScript (bring your own build)

Write `plugin.ts`, then transpile to the `plugin.js` Nocturn loads:

```sh
esbuild plugin.ts --bundle --format=iife --outfile=plugin.js
# or, without a bundler:
tsc plugin.ts --outFile plugin.js --target ES2020
```

With a bundler you can `import` npm packages — they are inlined into `plugin.js`.
`fetch`/`fs` still resolve to Nocturn's gated runtime at execution time.

## The manifest (`plugin.json`)

- `tools[]` — each `{name, description, parameters (JSON Schema), intent?, consequential?}`.
  `intent` is the install-reviewed HITL wording (`{field}` placeholders from args).
- `cage[]` — the upper bound on what any tool may reach: `{family, target, access}`
  where `family` is `http`|`file`|`dns`, `target` is a host glob (http) or path glob
  (file), and `access` is `["read"]`, `["write"]`, or both. Effects outside the cage
  are hard-denied without even asking.
- `credentials[]` / `oauth[]` — optional host-injected auth (the guest never sees the token).

## Install

Drop the plugin directory under a workspace's `plugins/` folder; Nocturn reviews the
cage once at startup, then gates each effect at run time (you choose once / session /
always per effect).
