# Go review — app/agent (agent.go, discover.go, scheduler.go, set.go)

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 1 minor · 2 nits

Tooling: `gofmt -l` clean · `go vet ./app/agent` clean. Banned words: none found.

## Minor
### app/agent/scheduler.go
- **L40–46** — scheduler blocks on synchronous fire
  - **Rule:** decisions.md §Goroutine lifetimes: "it is important to document when and why the goroutines exit."
  - **Found:** `tick` calls `s.fire(ctx, a)` inline in the `Start` loop; the workspace's fire is `FireAgent`, which blocks up to `turnTimeout` (~2 min). A slow/multiple firing stalls the loop past the next minute boundary, so the next `time.Until(next)` is already negative and ticks drift/bunch.
  - **Suggest:** Bound or document it — preferred `go s.fire(ctx, a)` inside `tick` (fire already honors `ctx`).

## Nits
### app/agent/set.go
- **L5** — `Set` documented "immutable" but is a mutable `map[string]Agent` that `Discover` writes to; nothing stops a holder mutating it. Reword to "read-only by convention."

### app/agent/agent.go
- **L28** — `Matches` prefix/group semantics are non-obvious; an inline example would help. (No style rule → nit.)

## Good
- Doc comments identifier-led and present throughout; receivers consistent per type.
- `Scheduler.Start` stops its timer on `ctx.Done()` with no timer leak.
