# Documentation conventions — Capabilities & Tools

Source of truth for how the reference docs are structured. When you add or change a capability or
a tool, follow this exactly so the pages stay **uniform** and **complete**. The docs-update hook
(`.claude/settings.local.json`) points here.

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

## 2. File & sidebar layout

- **Capability pages:** `src/content/docs/reference/<family>.md` (`http.md`, `files.md`,
  `reminders.md`, …).
- **Tool pages:** `src/content/docs/reference/tools/<tool>.md`, dots in the tool name → hyphens
  (`http.write` → `http-write.md`).
- **Sidebar** (`astro.config.mjs`):
  - **Capabilities** section = Overview (`capabilities`) + one entry per capability family.
  - **Tools** section = tool pages **grouped by capability** (HTTP, DNS, …, and an "Other" group
    for ungated tools).
  - **No flat "Tools overview" page** — the grouped sidebar + the capability pages are the index.

## 3. Capability page — required structure

Sections, in order (include those that apply):

1. Frontmatter `title` / `description`.
2. Intro — one paragraph: what authority it lends.
3. `## At a glance` — table: Family · Target · Tools · Default policy.
4. `## Tools` — **links only** to the tool pages (no schemas), each with its read/write tag.
5. `## Cage syntax` — how to cage this capability, with `{family, target, access}` examples. For a
   **host-owned** capability (`notify`/`remind`) it is **family-level** (`target: "*"`), not per-target.
6. `## Limits` — **required on every capability page.** The **rate limit** is enforced **per
   capability family, not per tool** (`RateLimiter.Allow(call.Family)`). State the concrete cap, or
   **TBD** while it is unwired (today the rate limiter is a primitive not yet attached to the Guard,
   so it is TBD). Use this exact section on every capability page so it stays uniform. **Size /
   resource caps also belong here** (e.g. response-body cap for `http`, single-read cap for
   `file`) — they are limits, not scanning or confinement details.
7. `## Credentials` — **only if the capability injects secrets** (`http`, and MCP-over-http).
   Host-side bearer injection at the boundary; the guest never sees the token; a guest-supplied
   credential (URL userinfo or an auth header) is rejected.
8. `## Leak scanning` — **only if the capability scans.** Describe **what is scanned** and **what is
   stripped**. `http`: egress scan of URL + headers + body, ingress scan of response body + header
   values, and credential headers stripped outright (`Set-Cookie`, `Authorization`, …).
   `notify`/`remind`: the message text is leak-scanned on egress.
9. Capability-specific detail as applicable: `## Confinement` (file), `## Requirements` (icmp),
   `## Why it is a gated capability anyway` / `## Why it runs silently`.

**Capability pages never contain tool input/output schemas** — those live on the tool pages.

## 4. Tool page — required structure (uniform)

Every tool page, in this order:

1. Frontmatter `title` (the tool name) / `description`.
2. `**Capability:** [<family>](/reference/<family>/) · <axis tag>` — or `**Capability:** — (ungated)`.
3. One sentence: what it does (+ "reach/cage/credentials live on the capability page").
4. `## Input` — a table (or `None.`).
5. `## Output` — JSON or a short description.
6. `## From JavaScript` — see the completeness rule in §6.
7. Optional `## Notes` / a capability-specific caveat.

## 5. Read/Write tags

Use the shared badge **everywhere** the axis appears (capability tables, tool `## Tools` lists,
tool-page headers): `<span class="axis axis--read">read</span>` /
`<span class="axis axis--write">write</span>` (styled in `src/styles/brand.css`). Never plain
`read` / `(read)` / `— read`.

## 6. JavaScript completeness rule (important)

Every tool's `## From JavaScript` MUST show **two** forms:

- the **idiomatic wrapper** from the runtime prelude (`internal/script/runtime.js`), **and**
- the **generic gate**: `nocturn.call("<tool>", { … })`.

If a tool has **no wrapper**, that is a gap to close: **add the wrapper to
`internal/script/runtime.js`** (it is the JS prelude prepended at eval time — no wasm rebuild), then
document both forms. **A tool page that shows only `nocturn.call` means the wrapper is missing and
must be added.**

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

## 7. Completeness checklist — when adding a capability

- [ ] Capability page created per §3 (incl. cage syntax + credentials/leak-scan if secrets flow).
- [ ] One tool page per tool per §4, each with the §6 **two-form** JS block.
- [ ] A runtime wrapper exists for every tool (else add it to `internal/script/runtime.js`).
- [ ] Sidebar: capability under **Capabilities**; tools grouped under **Tools**.
- [ ] `capabilities.md`: family added to "The capabilities" table; ungated tools noted.
- [ ] Read/write tags used everywhere; `npx astro build` green; all internal links resolve.
