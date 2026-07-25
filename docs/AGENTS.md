# Working on the docs site

Astro + Starlight. `npx astro build` is the gate: it validates every YAML data entry against the Zod
schema in `src/content.config.ts` and fails on a bad field.

```bash
npx astro dev      # local, hot reload
npx astro build    # schema validation included — run this before claiming a change works
```

## The one rule

**This site documents the code in this repository, not a design of it.** Before writing a claim
about behaviour, check it against the source:

| Claim about | Check |
|---|---|
| a tool's name, arguments, output | `internal/tools/*.go` — the `agentkit.NewTool(...)` call |
| whether a tool asks | does it call `gate.Check`? Not every one does |
| the permission policy | `internal/workspace/workspace.go`, func `policy()` |
| gate kinds, recall, grants | `agentkit/gate` |
| what a script can call | `internal/script/prelude.js` |
| a plugin manifest field | `internal/plugin/manifest.go` (parsed with `DisallowUnknownFields`) |
| an MCP config field | `internal/mcp/config.go`, func `Validate` |
| CLI and chat commands | `cmd/nocturn/cli.go`, `cmd/nocturn/main.go` |
| the workspace layout | `internal/workspace/workspace.go` |

Describing the intent rather than the build is the failure mode here — it is how the site came to
document dotted tool names, six capability families and a permission rule none of which existed. If
code and docs disagree, the code wins and the doc is wrong.

## Structure

- `src/content/docs/**` — Markdown/MDX pages (guides, architecture, the gate overview).
- `src/data/tools/*.yaml`, `src/data/kinds/*.yaml` — the reference, data-driven. **Read
  `CONVENTIONS.md` before touching these**: the file name is the tool name, `gated` is required, and
  the render order is fixed.
- `src/components/` — `ToolPage.astro` and `KindPage.astro` render those entries;
  `ThemeProviderDark` / `ThemeSelectNone` force the dark-only theme.
- `src/styles/brand.css` — the palette, mirrored from `mobile/src/theme/variables.css`. Keep the two
  in sync; do not invent colours here.
- `astro.config.mjs` — the manual sidebar, mermaid, and the hero parallax script.

## Conventions that bite

- Custom routes (`/reference/tools/…`, `/reference/gate/…`) are `<StarlightPage>` pages, so the
  sidebar must reference them with `link:`, never `slug:`.
- Prose lives as Markdown inside YAML fields — never hand-written HTML.
- Badges are fields (`axis`, `gated`), not markup.
- `CLAUDE.md` is a symlink to this file. Edit this one.
