# Go review — internal/hitl (+ internal/hitl/ntfy), full-package audit

**Scope:** committed non-test source — `internal/hitl/{engine.go, token.go, serialize.go}` and `internal/hitl/ntfy/{ntfy.go, listener.go}` (5 files). Tests skimmed for context only.
**Verdict:** ship with nits
Findings: 0 blockers · 1 major · 3 minor · 5 nits

**Tooling:** `gofmt -l` clean on all 5 files · `go vet ./internal/hitl/...` clean.

**Security-core verdict (token/HMAC/fail-closed/concurrency):** correct. The trust boundary is enforced in the right order and the fail-closed defaults hold. No security blocker. Details in the "Good" section; the one Major below is defense-in-depth hardening, not an exploitable hole.

---

## Major

### internal/hitl/engine.go
- **L195–199 (`rand.Read` at L197)** — error from `crypto/rand.Read` discarded in the security-critical id/nonce generator.
  - **Rule:** `decisions.md` §crypto/rand shows the canonical pattern handling the error: `if _, err := rand.Read(buf); err != nil { log.Fatalf("Out of randomness, should never happen: %v", err) }`. And `decisions.md` §Handle errors: "It is not usually appropriate to discard errors using `_` variables. If a function returns an error, do one of the following: Handle and address the error immediately[; ] Return the error to the caller[; ] In exceptional situations, call `log.Fatal`."
  - **Found:** `randID` does `_, _ = rand.Read(b)` and returns `hex.EncodeToString(b)`. On a (contractually impossible in Go ≥1.24, where `crypto/rand.Read` panics internally rather than returning an error) entropy failure, `b` stays all-zero, yielding a predictable, colliding id **and** nonce. This is the pending-map key and the single-use nonce for the whole HITL token scheme — exactly the value the guide's example guards. Real-world impact is low (tokens are still HMAC-signed, so a predictable nonce does not let an attacker forge an approval; the only degradation is map-key collision between two concurrent requests), but this is the TCB and the guide addresses the exact call directly.
  - **Suggest:** make the impossibility explicit rather than silent.
    ```go
    func randID() string {
        b := make([]byte, 16)
        if _, err := rand.Read(b); err != nil {
            panic("hitl: out of randomness: " + err.Error()) // crypto/rand never fails; make it loud if it ever does
        }
        return hex.EncodeToString(b)
    }
    ```

---

## Minor

### internal/hitl/engine.go
- **L139** — `p` shadowed by a different type inside `Request`.
  - **Rule:** `decisions.md` §Local consistency / readability: names should not mislead the reader; reusing an in-scope identifier for an unrelated type within the same function hurts local reasoning (the guide's general clarity stance; there is no hard normative ban, so this is minor).
  - **Found:** the outer `p := &pending{...}` (L121, read again at L160 `<-p.resolved`) is shadowed by `if p := deadline.PauserFrom(ctx); p != nil` — a `*deadline.Pauser`. In a 200-line security file the same one-letter name naming two unrelated things across the function body is avoidable friction. Functionally correct (inner `p` is block-scoped).
  - **Suggest:** rename the pauser, e.g. `if pauser := deadline.PauserFrom(ctx); pauser != nil { pauser.Pause(); defer pauser.Resume() }`.

- **L119 vs L157** — the request TTL is anchored to two different epochs.
  - **Rule:** `guide.md` clarity — a single logical deadline should have a single origin; two origins for "how long this request lives" invites drift.
  - **Found:** token `expires` is `time.Now().Add(ttl)` computed at L119 (before `Notify`), while the wait window is `context.WithTimeout(ctx, ttl)` created at L157 (after `Notify`). If `Notify` I/O is slow, the wait window outlives the token: a genuine tap arriving in that tail is rejected by `verifyToken` as expired even though the request is still parked. Fail-closed, so no security issue — but a user-visible "your approval was too late" when it was not. Worth aligning both to the same `expires` instant (e.g. `context.WithDeadline(ctx, expires)`).

### internal/hitl/ntfy/listener.go
- **L58–70** — `Run` returns `error` but can only ever return `nil`.
  - **Rule:** `decisions.md` §Returning errors: "Use `error` to signal that a function can fail." A signature that always returns `nil` signals a failure mode that does not exist and forces every caller into a dead `if err != nil`.
  - **Found:** both return sites (`ctx.Err() != nil` and `<-ctx.Done()`) return `nil`; the only real error (`stream`) is deliberately swallowed for reconnect. The `error` result is vestigial. Either drop it (`func (l *Listener) Run(ctx context.Context)`) or return `ctx.Err()` so cancellation is observable. Minor because the run-until-cancel convention makes an `error` return defensible for future use.

---

## Nits

### internal/hitl/engine.go & internal/hitl/token.go
- **engine.go L180; token.go L46, L50, L54, L59, L64, L68, L71, L75** — `fmt.Errorf` with no formatting verbs. `errors.New` is the idiomatic form for a static message (staticcheck S1028; no normative rule in the four reference docs, hence a nit). Add `"errors"` and switch, e.g. `return token{}, errors.New("hitl: malformed token")`.

### internal/hitl/ntfy/ntfy.go
- **L91** — `Notify` hardcodes `context.Background()`, so cancellation of the originating request cannot abort the push. This is forced by the `hitl.Notifier` interface (no `ctx` param) and bounded by `client.Timeout` (10s), so it is acceptable; noting only that the interface, if ever revised, should carry a `ctx`.
- **L124–133 (`post`)** — the response body is closed but not drained before close. Draining (`io.Copy(io.Discard, resp.Body)` before `Close`) lets `http.Client` reuse the keep-alive connection. Not a style-guide rule; micro-efficiency nit.

### internal/hitl/token.go
- **L62** — `strings.Split(string(payload), "|")` is safe today only because every field (hex id/nonce, decimal ints) is separator-free; the HMAC check at L58 already runs first, so a tampered payload never reaches parsing. No change needed — flagging the implicit invariant so a future field that could contain `|` gets length-prefixed or escaped instead.

### internal/hitl/ntfy/listener.go
- **L45** — `&http.Client{}` with no timeout is correct here (long-lived stream, stopped via ctx) and the comment says so; noting only that there is no idle/read-header timeout, so a wedged-open-but-silent server holds the goroutine until ctx cancel. Acceptable for a reconnecting subscriber.

---

## Good (security-core, verified)
- **Verify-before-trust ordering** — `token.go` L56–60 computes the HMAC and runs `hmac.Equal` (constant-time) *before* `strings.Split` parses any field (L62). The outcome is only read from a payload whose signature already checked out; a `Deny` payload cannot be spliced onto an `Approve` signature. `TestToken_TamperRejected` pins this.
- **Fail-closed defaults** — `Outcome` zero value is `Denied` (engine.go L30); `Request` returns `Denied` on notify error, timeout, and cancellation (L154, L164); `verifyToken` rejects `nowUnix >= expires` (token.go L70), matching `TestToken_ExpiredRejected`.
- **Single-use consumption is race-safe** — `Resolve` deletes from the pending map under `mu` *before* delivering (engine.go L182), and `discard` deletes under the same lock; a timed-out and a late-arriving `Resolve` cannot both fire. The `resolved` channel is buffered (cap 1, L121) so `Resolve`'s send never blocks even after the waiter has left — no goroutine leak.
- **No lock held across blocking I/O** — `Notify` (L152) and the select wait run with `mu` unheld; the map is only touched under lock. `serialNotifier` (serialize.go) correctly serializes prompts, is idempotent, and passes `nil` through.
- **Deadline hygiene** — the HITL wait pauses the execution budget (engine.go L139–142) so a slow human does not burn the guest/tool deadline, with `defer Resume` balancing both the notify-error early return and normal exit.
