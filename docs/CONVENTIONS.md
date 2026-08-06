# Documentation conventions — gate kinds & tools

Source of truth for how the reference docs are structured. Kind and tool pages are **data-driven**:
you author a **YAML data entry**, and a shared Astro component renders the fixed layout. The
structure is therefore **enforced** — a Zod schema + the renderer make it impossible to reorder,
omit a required part, or drift. A malformed entry **fails `npx astro build`**.

> Do **not** write these pages as Markdown `.md` files, and do **not** hand-write HTML
> (`<span class="axis…">`, `<code>`, `<a>`) — author Markdown *in the YAML fields*; the component
> renders it.

---

## 1. The model these pages describe

Two separate questions, and the docs must never blur them:

- **Cage** — which tools a caller has at all (`agentkit.ToolSet`, a plugin's `uses`, an agent's
  `tools`). Not a rule that is evaluated: an absent tool is absent.
- **Gate** — what a call may do, checked per call as `gate.Check(Action{Kind, Target})`.

**Kinds today** — exactly five, and no page may invent a sixth: `net` (`tools.NetKind`), `file`
(`tools.FileKind`), `notify` (`tools.NotifyKind`), `remind` (`tools.RemindKind`), `memory`
(`memory.Kind` — it lives in `internal/memory`, which is why it is easy to miss).
Verify with `grep -rn 'Kind = "' internal/tools internal/memory`.

**Gated is not derivable from the tool's name or its axis.** `http_read` is a read and asks;
`file_read` is a read and never calls the gate. Every tool entry states `gated` explicitly.

## 2. Where the pages live

| | Data entry | Rendered by | URL |
|---|---|---|---|
| **Tool page** | `src/data/tools/<tool>.yaml` — file name **is** the registered tool name, underscores and all | `<ToolPage>` via `src/pages/reference/tools/[...slug].astro` | `/reference/tools/<tool>/` |
| **Kind page** | `src/data/kinds/<kind>.yaml` | `<KindPage>` via `src/pages/reference/gate/[...slug].astro` | `/reference/gate/<kind>/` |

- **Schema** lives in `src/content.config.ts` (`tools` and `kinds` collections). It is the contract —
  the build fails on a missing or invalid field.
- **Still Markdown** (`docs` collection): `reference/gate.md`, `reference/wasm-abi.md`,
  `reference/javascript-runtime.md`, all `guides/*` and all `architecture/*`.
- **Sidebar** (`astro.config.mjs`): kind and tool entries use **`link: '/reference/…/'`** — they are
  `<StarlightPage>` custom routes, not `docs` slugs, so `slug:` would fail the build. Markdown pages
  keep `slug:`.

## 3. Authoring the fields

- **Prose = Markdown, never HTML.** Inline fields (`notes`, bullets, `desc`, `output` prose, grant
  notes) take **inline** Markdown. Block fields (`intro` on a kind, `grants.intro`, `sections[].body`)
  take **block** Markdown.
- **Code goes in code fields**: `js.wrapper` / `js.call`, tool `output.code`, `grants.examples`.
- **Badges are fields**, never markup: `axis` (`read`/`write`/`none`) and `gated` (boolean) render
  themselves.

## 4. Tool entry — `src/data/tools/<tool>.yaml`

Fields: `title` (the registered name), `description`, `kind` (`{name, href, target?}` or `null`),
`gated` (bool, **required**), `axis`, `intro`, `input` (array of `{field, type, required, notes}` or
`'none'`), `output` (string **or** `{text?, code?, lang, after?}`), `js` (`{wrapper: string|null,
call: string}`), `notes?`.

Fixed render order: **gate line + axis badge → intro → `## Input` → `## Output` →
`## From JavaScript` → `## Notes`**.

`kind` is about **belonging**, `gated` about **behaviour**. A file read sets `kind: file` with
`gated: false` and omits `target`; a gated tool sets `target` to whatever `Action.Target` carries
for it.

`input` must mirror the tool's real `agentkit.WithSchema` — field names, requiredness and enums.
Check against `internal/tools/*.go` rather than memory.

## 5. Kind entry — `src/data/kinds/<kind>.yaml`

Fields: `title`, `description`, `kind` (the constant's value), `constant` (the Go identifier),
`intro`, `glance` (`{target, policy, matcher}`), `tools` (array of `{name, href, axis, gated, desc?}`),
`grants` (`{intro, examples, lang, notes[]}`), `limits[]` (min 1), `credentials?[]`, `leakScanning?`,
`sections?`.

Fixed order: **At a glance → Tools → Grants → Limits → [Credentials] → [Leak scanning] → freeform
`sections`**.

- **`glance.policy`** must match `internal/workspace/workspace.go:policy()` verbatim in substance.
  `net`/`file` ask with session recall; everything else is allowed.
- **`grants.examples`** are real `gate.Grant` JSON — `{"kind": …, "target": …}` — as written to
  `grants.json`. Never invent a shape.
- **`limits`** are the caps that hold regardless of any approval, with the real numbers
  (`maxBody` = 64 KiB, `maxFileBytes` = 1 MiB, `maxSearchResults` = 500).
- **`credentials`** only where secrets are injected (`net`).
- **`leakScanning`** only where scanning happens (`net`, `notify`, `remind`).

**Anchor stability:** ids come from `github-slugger` (`#at-a-glance`, `#tools`, `#grants`, `#limits`,
`#credentials`, `#leak-scanning`, plus slugified `sections` titles). Other pages deep-link to these.

## 6. JavaScript completeness rule (schema-enforced)

Every tool's `js` block must provide both forms:

- **`js.call`** — the generic `nocturn.call("<tool>", { … })` form — is **required**.
- **`js.wrapper`** — the idiomatic prelude form — is **nullable**. A wrapper-less tool must set
  `wrapper: null` on purpose, and that `null` is the signal to consider adding one to
  `internal/script/prelude.js` (prepended at eval time — no wasm rebuild).

Wrapper coverage as of today — verify with
`grep -o 'nocturn\.call("[a-z_]*"' internal/script/prelude.js | sort -u`:

| Tool(s) | Wrapper |
|---|---|
| `http_read` / `http_write` | `fetch(...)` |
| `file_*` | `nocturn.fs.*` and the `require("fs")` shim |
| `dns_resolve` | `nocturn.resolve(host, type?)` |
| `ping` | `nocturn.ping(host)` |
| `time_now` | `nocturn.now()` |
| `notify` / `remind` | `nocturn.notify(...)` / `nocturn.remind(...)` |
| `wake` | `nocturn.wake(seconds, note)` |
| `skill_read` | `nocturn.skillFile(skill, path)` |
| `remind_list` / `remind_cancel` / `code_run` | none — `wrapper: null` |

The prelude's tool names are covered by `TestPrelude_WrappersDispatchToRegisteredNames`
(`internal/script/script_test.go`). If you add a wrapper, add its case there — the whole point is
that a wrapper naming a tool that does not exist fails a test instead of failing a user.

## 7. Checklist — adding a tool or a kind

- [ ] YAML entry created with every required field; `gated` matches whether the tool really calls
      `gate.Check`.
- [ ] `input` matches the tool's schema in `internal/tools/`.
- [ ] `js.call` set; `js.wrapper` set **or explicitly `null`**.
- [ ] The kind page's `tools[]` lists it, with the same `axis` and `gated`.
- [ ] Sidebar (`astro.config.mjs`): a **`link:`** entry under the right group.
- [ ] The file name equals the registered tool name — verify:
      `for f in docs/src/data/tools/*.yaml; do grep -rq "\"$(basename $f .yaml)\"" internal agentkit || echo "$f"; done`
- [ ] `npx astro build` green; internal links and anchors resolve.
