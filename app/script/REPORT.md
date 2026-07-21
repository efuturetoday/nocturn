# Go review — app/script (guest.go, prelude.go, script.go)

**Verdict:** ship
Findings: 0 blockers · 0 major · 0 minor · 0 nits

Tooling: `gofmt -l` clean · `go vet ./app/script` clean. Banned words: none found.

All exported and non-obvious unexported declarations have doc comments beginning with the declared name (decisions.md §Doc comments). Receiver `r` consistent across `Runner` methods. `sync.OnceValues` for the process-wide engine is idiomatic lazy init with cached failure, well documented. `dispatch` operates only on the already-copied `req` and refuses `code_run` re-entry.

## Good
- script.go:41–45 once-compiled shared interpreter, concurrency-safe, zero cost when unused.
- guest.go:61–84 single-gate `dispatch` routes every script action through the same `agentkit.ToolSet.Call` the model uses, errors surfaced as guest-visible strings.
