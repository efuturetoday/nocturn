# Go review — app/sandbox (engine.go, sandbox.go)

**Verdict:** needs changes
Findings: 0 blockers · 1 major · 0 minor · 1 nit

Tooling: `gofmt -l` clean · `go vet ./app/sandbox` clean.

## Major
### app/sandbox/sandbox.go
- **L8 (×2), L40** — Banned lexicon term "effect" in doc comments.
  - **Rule:** House style — words "capability", "effect", "axis", "creds" are banned; flag any occurrence.
  - **Found:** L8–9 package doc `// The sandbox performs NO effect itself — every effect is a HostFunc`; L40 `HostFunc` doc `// the sandbox never performs an effect itself.`
  - **Suggest:** reword to "action", e.g. "The sandbox performs NO action itself — every action is a HostFunc supplied by the caller."

## Nits
### app/sandbox/engine.go
- **L107** — `Run` closes the fresh instance with parent `ctx` (`mod.Close(ctx)`) rather than `runCtx`. Intentional and correct (release even if the budget expired); noted only for the reader.

## Good
- engine.go:160 `append([]byte(nil), view...)` copies the transient `Memory.Read` view before the host fn returns — the wazero view-not-copy pitfall handled correctly.
- Per-call dispatchers ride `ctx` via unexported `hostsKey{}` (L121, L133) so one compiled host module is shared across concurrent instantiations with no per-call trampoline state.
- Fail-loud host check (L80–87) + fail-closed nil-dispatcher branch (L152–159) match deny-by-default.
