# Go review — internal/capability (full-package audit, committed code)

Scope: all non-test .go files in internal/capability/ — access.go, autonomy.go, broker.go, cage.go, epoch.go, grants.go, ratelimit.go, window.go (8 files). Test files skimmed for context.

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 1 minor · 3 nits

Tooling: `gofmt -l` → clean. `go vet ./internal/capability/...` → clean.

This is the security TCB core. Correctness, fail-closed behavior, and concurrency-safety were weighted highest and are, on this read, sound: deny-by-default and deny-wins are structurally enforced (`Policy.decide`), every "forgotten field" (empty Family, empty TargetGlob, MatchNone, Epoch 0, nil Env.Epochs) resolves to no-match / deny, and all shared mutable state (EpochRegistry, Grants.session, RateLimiter.events) is mutex-guarded. No blocker- or major-severity issues found.

## Minor

### internal/capability/ratelimit.go
- **L15** — Stale doc reference to a field that does not exist (`Env.RateAllow`)
  - **Rule:** decisions.md §Comment Line Length / Commentary intent — comments are normative documentation and must describe the code as it is; and best_practices.md §Documentation: doc comments are the API contract readers rely on. A comment naming a non-existent field misdirects. (Effective Go §Commentary: "Doc comments work best as complete sentences" — and, implicitly, accurate ones.)
  - **Found:** The `RateLimiter` doc says "The broker consults it on every authorized path via `Env.RateAllow`." But `Env` (broker.go:233) has only `Now` and `Epochs`, and its own doc explicitly states "Rate limiting is deliberately NOT here … it lives in the gateway". The rate limiter is consulted via `gateway.Guard.Rate`, not an `Env.RateAllow`. The reference is dead/contradictory.
  - **Suggest:**
    ```go
    // file.read) pass freely. The gateway consults it on every authorized path
    // (gateway.Guard.Rate); it is deliberately not part of the pure Env.
    ```

## Nits

### internal/capability/ratelimit.go
- **L23-24, L33** — Vocabulary drift `window` vs `per`. `WithLimit` names its parameter `window time.Duration` but stores it as `rateCfg.per`; the doc and error text both say "window". Not wrong (the field doc explains it), but one word for one concept reads cleaner. Preference only.

### internal/capability/broker.go
- **L272-274** — `Policy.Evaluate` is a one-line pass-through to unexported `decide`. The split buys separate doc surfaces (public contract vs precedence mechanics) which is defensible, but a reader may wonder why both exist. Consider folding the precedence comment onto `Evaluate` and dropping `decide`, or keep as-is — no rule compels either. Preference only.

### internal/capability/broker_test.go
- **L11** — Local helper closure named `cap` shadows the predeclared builtin `cap`. Harmless in a test, but shadowing a builtin identifier is a readability smell; `mkCall` / `call` would avoid it. Test-only, preference.

## Good
- **Fail-closed is structural, not incidental.** `Match.covers` (broker.go:41) `default: return false`, `Rule.matches` empty-family/empty-glob/Epoch-0 branches, and `Policy.decide`'s terminal `default: return Deny` all make "forgot to set it" mean "grants nothing". Matches the CLAUDE.md §6 fail-closed mandate exactly.
- **Concurrency discipline.** `Grants.Allows` (grants.go:76-78) snapshots `session` under its own lock, then runs `rule.matches` (which may call `EpochRegistry.IsAlive`, a second lock) OUTSIDE the Grants lock — avoids nested lock-ordering hazards. `RateLimiter.Allow` reads the never-mutated `limits` map lock-free with a comment justifying the happens-before (construction publishes before any call), and takes the mutex only for the mutable `events`. Denied calls are not recorded (ratelimit.go:81-82), so being over the limit never pushes the window forward — verified by test.
- **crypto/DoS-adjacent care:** `targetMatches` (broker.go:204) deliberately refuses to resolve DNS in the pure decision layer and documents why (effect + race). CIDR/IP targets are matched numerically so IPv6 textual spellings can't slip past a string glob.
- **Tests are exemplary:** table-driven got/want (`TestPolicy_Evaluate`), fail-closed negative cases named explicitly, and time-dependent sliding-window tests use `testing/synctest` with real `time` rather than a clock seam — precisely the convention in CLAUDE.md §9.
