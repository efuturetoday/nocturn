# Go review — app/plugin (discover.go, manifest.go, plugin.go)

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 1 minor · 2 nits

Tooling: `gofmt -l` clean · `go vet ./app/plugin` clean. Banned words: none found.

## Minor
### app/plugin/manifest.go
- **L124** — `allows` doc comment begins with the wrong name.
  - **Rule:** decisions.md §Doc comments: "these comments should be full sentences that begin with the name of the object being described."
  - **Found:** comment above `func (m Manifest) allows(...)` opens `// uses reports whether…` — names `uses`, not `allows` (stale rename).
  - **Suggest:** `// allows reports whether the guest may dispatch to a base tool of the given name: …`

## Nits
### app/plugin/manifest.go
- **L173–178** — `Load` returns a partially-filled `Loaded` alongside a non-nil error on the WASM/JS read paths (`return Loaded{Manifest: m, Artifact: b, Kind: KindWASM}, err`). Harmless (callers check `err` first); a zero `Loaded{}` on the error path reads cleaner. No normative rule → preference.

### app/plugin/plugin.go
- **L76** — `name := td.Name` is a redundant per-iteration copy under go 1.26 (loop vars already per-iteration since go1.22). Preference / `golang-modernize`.

## Good
- `dispatchCall`/`run` re-scope credential injection per plugin via `secret.WithOwner` and refuse re-entry of `code_run`/own tools.
- `rawArgs` re-marshals guest args to a clean literal (injection defense).
