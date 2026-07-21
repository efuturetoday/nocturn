# Go review — app/chat (manager.go, store.go)

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 1 minor · 2 nits

Tooling: `gofmt -l` clean · `go vet ./app/chat` clean. Banned words: none found.

## Minor
### app/chat/manager.go
- **L55–61** — `NewID` panics on `crypto/rand` failure
  - **Rule:** decisions.md §Don't panic: "Do not use `panic` for normal error handling. Instead, use `error` and multiple return values."
  - **Found:** `NewID` is an ordinary exported value-returning func (called from `Start`), not a `Must`-named startup/`init` helper, yet panics. Idiomatic shape is `(string, error)` propagated through `Start`. House comment argues catastrophic-failure justification — defensible, but flag it as the sole sanctioned panic if kept.

## Nits
### app/chat/store.go
- **L64** — `OnSave` writes `s.onSave` without `s.mu` while `fireSaved` reads it outside the lock. Documented "set once at wiring time" so no practical race; making it a constructor `Option` (like `WithSource`) would make single-assignment structural.
- **L187–206** — `Rename`/`Delete` mutate a chat but don't call `fireSaved`, unlike `Save`/`MarkRead` — inconsistent broadcast; a one-line comment would settle intent.

## Good
- Doc comments identifier-led and present; receivers consistent per type.
- Persistence uniformly atomic write-then-rename at 0600 (dirs 0700); `Store.Save` correctly fires its callback outside the mutex.
