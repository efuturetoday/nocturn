# Go review — app/auth (auth.go, join.go, otp.go)

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 0 minor · 1 nit

Tooling: `gofmt -l` clean · `go vet ./app/auth` clean. Banned words: none found. Every bearer/code comparison uses `subtle.ConstantTimeCompare` on fixed-length operands; bearers are 256-bit crypto/rand; `otpCode` uses `rand.Int(rand.Reader, 1e6)`.

## Nits
### app/auth/otp.go
- **L33** — bootstrap `otp.valid` has no wrong-attempt guard, unlike `join` (`ConfirmJoin` counts attempts and drops past `joinMaxTries=5`). No style rule applies (cite-or-drop → nit); security observation. Per git log `FRAGEN #22` this is an **accepted** decision ("no lockout guard; --reset-pairing is the accepted recovery"). Recorded for traceability, not a defect.

## Good
- Every bearer/code comparison uses `subtle.ConstantTimeCompare` on fixed-length operands (64-hex hashes / 6-digit codes) — genuinely constant-time.
- Bearers are 256-bit crypto/rand base64url, stored SHA-256-only; `newBearer` returns an error while `newID` panics — deliberate and documented.
- `save` is write-tmp-then-rename at `0o600`; join state is transient with TTL + prune-on-read; `PendingJoins` returns `[]PendingJoin{}` to keep the wire `[]` not `null`.

One cross-file note: `auth.go:129 PushTargets` returns a nil slice (→ JSON `null`) whereas `PendingJoins` deliberately avoids null; harmless since `PushTargets` appears internal (feeds the push Sender), not wire-serialized — dropped from findings on that basis.
