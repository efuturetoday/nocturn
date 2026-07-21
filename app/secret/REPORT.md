# Go review — app/secret (store.go, master.go, credential.go, leakscan.go, vault.go)

**Verdict:** ship with nits
Findings: 0 blockers · 1 major · 1 minor · 1 nit

Tooling: `gofmt -l app/secret/*.go` clean · `go vet ./app/secret` clean.
Security spot-checks: `crypto/rand` used everywhere (master.go:123, vault.go:181) — no `math/rand`; AES-256-GCM with fresh per-seal nonce + version-binding AAD; fail-closed error shapes throughout; GCM-tag mismatch → `ErrWrongPassphrase`; every exported symbol has a name-prefixed doc comment. No zeroize/secret-wipe — documented as a deliberate Go-language-limit deferral in CLAUDE.md §M4, not flagged.

## Major
### app/secret/credential.go
- **L226** — banned house-style word "effects" in a doc comment
  - **Rule:** house style: "capability", "effect", "axis", "creds" are BANNED — flag ANY occurrence.
  - **Found:** `// injected on effects from that guest is limited to the plugin's own bindings`
  - **Suggest:** `// injected on requests from that guest is limited to the plugin's own bindings`
  - Only occurrence in scope; grep of all five files is otherwise clean of the four banned words.

## Minor
### app/secret/store.go
- **L12–L14** — two separate single-package `import` declarations instead of one grouped stdlib block
  - **Rule:** decisions.md §Import grouping: "Imports should be organized into the following groups, in order: 1. Standard library packages …" (gofmt leaves separate `import` statements untouched, so `gofmt -l` does not catch this).
  - **Found:** `import "maps"` then `import "sync"`
  - **Suggest:**
    ```go
    import (
        "maps"
        "sync"
    )
    ```

## Nits
### app/secret/master.go
- **L124** — `NewMasterSalt` returns the bare `rand.Read` error with no package context
  - **Rule:** best_practices §Error handling — add attributable context; the rest of the file prefixes `"secret: …"` (master.go:65, 138, 141). Preference, not normative.
  - **Suggest:** `return nil, 0, fmt.Errorf("secret: master salt: %w", err)`

## Good
- Guest boundary airtight by construction: no exported `*Store` method returns a value; internal reads unexported; `GuestView` exposes only `Exists`, with compile-time proof `var _ GuestView = (*Store)(nil)` (store.go:86).
- `hostMatches` fail-closed: empty/`"*"` matches nothing; `*.suffix` excludes bare domain (credential.go:249).
- `InjectMatching` snapshots (binding, resolver) under lock, does refreshing I/O outside it (credential.go:184–207).
- `applyRedactions` absorbs partial-overlap tails rather than skipping — the subtle leak case is covered (leakscan.go:354–362).
- Vault `Set` persists before mutating memory, so disk and memory never diverge (vault.go:133–136).
