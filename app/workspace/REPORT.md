# Go review — app/workspace (agents.go, grantstore.go, workspace.go)

**Verdict:** block
Findings: 1 blocker · 0 major · 0 minor · 2 nits

Tooling: `gofmt -l` clean · `go vet ./app/workspace` clean. Banned-word scan: one hit — "effect" in grantstore.go:50.

## Blockers
### app/workspace/agents.go
- **L47–69** — data race on `answer` (`strings.Builder`)
  - **Rule:** decisions.md §Goroutine lifetimes: "Modifying still-in-use inputs 'after the result isn't needed' can lead to data races … goroutine lifetimes [must be] obvious."
  - **Found:** The streaming goroutine calls `answer.WriteString(e.Text)` while ranging `sess.Subscribe()`. On the `case <-ctx.Done():` branch, `FireAgent` evaluates `answer.String()` for the return value *before* the deferred `sess.Close()` runs — the goroutine is still live and may be writing concurrently. `strings.Builder` is not concurrency-safe → real race (`go test -race` on a cancellation fires). Only the `<-done` branch has a happens-before edge.
  - **Suggest:**
    ```go
    case <-ctx.Done():
        sess.Close()   // deferred Close then no-ops
        <-done         // wait for the goroutine to finish writing answer
        return answer.String(), ctx.Err()
    ```

## Nits
### app/workspace/grantstore.go
- **L50** — banned word in comment: "never blocks the effect." → reword (e.g. "never blocks the write").

### app/workspace/workspace.go
- **L266–275** — `resolvePersona` collapses *every* `os.ReadFile` error to `defaultPersona`, so a permission/IO error on an existing `PERSONA.md` silently yields the wrong identity.
  - **Rule:** best_practices §Error handling — don't discard a non-NotExist error silently.
  - **Suggest:** distinguish `errors.Is(err, os.ErrNotExist)` from real errors.

## Good
- Doc comments identifier-led and present; receivers consistent per type.
