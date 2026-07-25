# Documentation conventions — Capabilities & Tools

Source of truth for how the reference docs are structured. Capability and tool pages are
**data-driven**: you author a **YAML data entry**, and a shared Astro component renders the fixed
layout. The structure is therefore **enforced** — a Zod schema + the renderer make it impossible to
reorder, omit a required part, or drift. A malformed entry **fails `npx astro build`**. The
docs-update hook (`.claude/settings.local.json`) points here.

> Do **not** write these pages as Markdown `.md` files anymore, and do **not** hand-write HTML
> (`<span class="axis…">`, `<code>`, `<a>`) — author Markdown *in the YAML fields*; the component
> renders it.

---

## 1. Capability vs Tool — the core distinction

- A **capability** is the authority the broker gates: in code it builds a
  `capability.Call{Family, Target, Write}` and runs through `gateway.Do` / `Guard.Authorize`, so it
  **can be caged**. Quick code test: `grep -rn 'capability.Call{' internal` — a package that builds
  one *and* calls `gateway.Do` is a capability.
- A **tool** is what the model / `nocturn.call` invokes (a `tool.Tool` in the registry). A tool
  exercises **zero, one, or several** capabilities.
- **Capabilities today** (by family): `http`, `dns`, `icmp` (tool `ping`), `file`, `notify`, `remind`.
- **Ungated tools** (no capability, never gated): `code.run`, `skill.load`, `skill.read`,
  `time.now`, `wake`.

## 2. Where the pages live (data-driven)

| | Data entry | Rendered by | URL |
|---|---|---|---|
| **Tool page** | `src/data/tools/<tool>.yaml` (dots → hyphens: `http.write` → `http-write.yaml`) | `<ToolPage>` via `src/pages/reference/tools/[...slug].astro` | `/reference/tools/<tool>/` |
| **Capability page** | `src/data/capabilities/<family>.yaml` | `<CapabilityPage>` via `src/pages/reference/[...slug].astro` | `/reference/<family>/` |

- **Schema** lives in `src/content.config.ts` (`tools` and `capabilities` collections). It is the
  contract — the build fails on a missing/invalid field.
- **Still Markdown** (`docs` collection): the overview `reference/capabilities.md`,
  `reference/wasm-abi.md`, `reference/javascript-runtime.md`, and all `guides/*`.
- **Sidebar** (`astro.config.mjs`): capability and tool entries use **`link: '/reference/…/'`**
  (they are `<StarlightPage>` custom routes, not `docs`-collection slugs — `slug:` would fail the
  build). The Overview and the Markdown pages keep `slug:`. Tools are grouped by capability; the
  capabilities are listed by family under **Capabilities**.

## 3. Authoring the fields (both page types)

- **Prose = Markdown, never HTML.** Inline fields (`notes`, bullets, `desc`, `output` prose, cage
  notes) take **inline** Markdown (`` `code` ``, `[text](href)`, `**bold**`). Block fields
  (capability `intro`, `cage.intro`, `sections[].body`) take **block** Markdown (paragraphs, lists).
- **Code goes in code fields**, never pasted as prose: `js.wrapper` / `js.gate`, tool `output.code`,
  `cage.examples`. These render as real Expressive-Code blocks.
- **Axis is a field** (`read` / `write` / `none`), not markup — the badge renders itself. Never
  hand-write `<span class="axis…">`.

## 4. Tool entry — `src/data/tools/<tool>.yaml`

Fields: `title`, `description`, `capability` (`{family, href}` or `null` for ungated), `axis`
(`read`/`write`/`none`), `intro` (inline md), `input` (array of `{field, type, required, notes}` or
`'none'`), `output` (string **or** `{text?, code?, lang, after?}`), `js` (`{wrapper: string|null,
gate: string}`), `notes?`.

The component renders the fixed order and you cannot change it: **Capability line + axis badge →
intro → `## Input` → `## Output` → `## From JavaScript` → `## Notes`**. `output` prose renders as a
paragraph; `output.code` as a JSON box (with optional `text` before / `after` after). See §6 for
`js`.

## 5. Capability entry — `src/data/capabilities/<family>.yaml`

Fields: `title`, `description`, `family`, `intro` (block md), `glance` (`{target, defaultPolicy}`),
`tools` (array of `{name, href, axis, desc?}`), `cage` (`{intro, examples, lang, notes[]}`),
`limits[]` (min 1), `credentials?[]`, `leakScanning?` (`{intro?, points[]}`), `sections?` (array of
`{title, body}`).

Fixed, enforced order: **At a glance → Tools → Cage syntax → Limits → [Credentials] → [Leak
scanning] → freeform `sections`**. Content rules per section:

- **`glance`** — the At-a-glance table (Family = `family`, Target, Tools derived from `tools[]`,
  Default policy).
- **`cage`** — how to cage it, with a `{family, target, access}` example block. For a **host-owned**
  capability (`notify`/`remind`) it is **family-level** (`target: "*"`), not per-target.
- **`limits`** — the **rate limit** is enforced **per capability family, not per tool**
  (`RateLimiter.Allow(call.Family)`); state the cap or **TBD** while unwired (today it is a primitive
  not attached to the Guard → TBD). **Size/resource caps also go here** (http response-body cap,
  `file` single-read cap).
- **`credentials`** — include **only if the capability injects secrets** (`http`, MCP-over-http):
  host-side bearer injection at the boundary; the guest never sees the token; guest-supplied
  credentials are rejected.
- **`leakScanning`** — include **only if it scans**: *what is scanned* and *what is stripped*
  (`http`: egress URL+headers+body, ingress body+header values, stripped `Set-Cookie`/`Authorization`
  …; `notify`/`remind`: the message text on egress).
- **`sections`** — the capability-specific tail: `## Confinement` (file), `## Requirements` (icmp),
  `## Why it is a gated capability anyway` / `## Why it runs silently …` / `## Channel` (notify).

**Anchor stability:** heading ids are generated with `github-slugger` (`#cage-syntax`, `#credentials`,
`#leak-scanning`, `#limits`, and slugified `sections` titles like `#confinement`, `#requirements`).
Other pages deep-link to these — keep section titles stable so cross-page anchors hold.

## 6. JavaScript completeness rule (schema-enforced)

Every tool's `js` block MUST provide both forms; the schema makes it impossible to skip the gate:

- **`js.gate`** — the generic `nocturn.call("<tool>", { … })` form — is **required**.
- **`js.wrapper`** — the idiomatic prelude form — is **nullable**. A tool with no wrapper MUST set
  `wrapper: null` **on purpose**, and that `null` is the signal to **add the wrapper to
  `internal/script/prelude.js`** (the JS prelude prepended at eval time — no wasm rebuild). The
  renderer shows both as `<Tabs>`; with `wrapper: null` it shows only the gate.

### Wrapper coverage (keep current)

| Tool(s) | Wrapper |
|---|---|
| `http.read` / `http.write` | `fetch(...)` |
| `file.*` | `fs.*` / `nocturn.fs.*` |
| `ping` | `nocturn.ping(host)` |
| `time.now` | `nocturn.now()` |
| `notify` | `nocturn.notify(msg, title)` |
| `dns.resolve` | **missing → add `nocturn.dns(host, type)`** |
| `remind` / `remind.list` / `remind.cancel` | **missing → add `nocturn.remind*`** |
| `wake` | **missing → add `nocturn.wake(seconds, note)`** |

## 7. Checklist — when adding a capability or tool

- [ ] Create the YAML entry (`src/data/tools/<tool>.yaml` or `src/data/capabilities/<family>.yaml`)
      with every required field.
- [ ] Each tool: `js.gate` set; `js.wrapper` set **or explicitly `null`** (if `null`, add the wrapper
      to `internal/script/prelude.js`).
- [ ] Sidebar (`astro.config.mjs`): add a **`link:`** entry — tool grouped under its capability;
      capability under **Capabilities**. Add the family to the overview `capabilities.md` table.
- [ ] `npx astro build` green (the schema validates every entry); all internal links **and anchors**
      resolve.
