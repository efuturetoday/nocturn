# Go review — app/push (apns.go, push.go)

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 0 minor · 2 nits

Tooling: `gofmt -l` clean · `go vet ./app/push` clean. Banned words: none found. crypto/rand used correctly (ES256 signing via `ecdsa.Sign(rand.Reader, …)`).

## Nits
### app/push/apns.go
- **L176, L184** — bare `return err` (from `http.NewRequestWithContext` and `client.Do`) drops the `apns:` context the rest of the file consistently adds; these surface bare through `Send`'s `lastErr`. best_practices.md §Error handling: "consider adding information that you have but that the caller and/or callee might not." Suggest wrapping with `%w`.
- **L204** — `providerToken` holds `mu` across `signJWT`'s `ecdsa.Sign`; correct and negligible (once per ~50 min), preference only, no change needed.

## Good
- `signJWT` ES256-signs the SHA-256 of the signing input with `ecdsa.Sign(rand.Reader, …)` and fixed-width `r||s` via `FillBytes` — correct, crypto/rand.
- `switch err := a.push(…); err { case errBadToken: }` sentinel dispatch matches best_practices.md §Error structure's endorsed "Good" pattern (errBadToken returned unwrapped, so `==` is sound).
- `payload` writes caller `Data` before the reserved `aps` key so it can't be shadowed; response read via `io.LimitReader`.
