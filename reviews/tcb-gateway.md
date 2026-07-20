# Go review — internal/gateway (full-package audit, committed code)

Scope: all non-test .go in internal/gateway/ (`gateway.go`, `scope.go`); 2 files, ~460 LOC.
Tooling: `gofmt -l` clean · `go vet ./internal/gateway/...` clean.

**Verdict:** ship
Findings: 0 blockers · 0 major · 1 minor · 2 nits

This is exemplary TCB code. The authorization pipeline is fail-closed by construction:
`Do`'s effect is a closure unreachable unless `Authorize` returns nil AND (for external
effects) the egress scan passes; the mandatory `EgressScan` argument makes an unscanned
external effect a compile-time impossibility; the never-auto floor, cage precedence, and
deny-wins ordering are all enforced before any grant/autonomy short-circuit. No bypass path
found. Receivers consistent (`g *Guard`, `s *Scope`, `e *RateLimitedError`), all exported
symbols documented, context keys unexported structs, `%q` used for the family string,
error strings lowercase and unpunctuated, `RateLimitedError` unwraps to `ErrDenied`.

## Minor

### internal/gateway/gateway.go
- **L271, L276** — grant-persistence error silently discarded
  - **Rule:** decisions.md §Errors / Handling: a returned error "must [be treated]" — the codebase's own §6 pattern is fail-closed, and Effective Go: "Do not discard errors." The `_ =` is the explicit-discard escape hatch, which is permitted, but the discard drops information with no sink.
  - **Found:** `_ = grants.Record(toolName, call, capability.ScopeAlways)` (and `ScopeSession` on L276). A failed persist means the human's "always"/"session" choice is silently lost. This fails *safe* (the next matching call simply re-prompts, never fails open), and the inline comment "persist error must not block the allow" documents the intent — hence Minor, not Major.
  - **Suggest:** design is defensible; if a logger is ever threaded into `Guard`, log the drop so a silently non-persisting GrantStore is observable:
    ```go
    if err := grants.Record(toolName, call, capability.ScopeAlways); err != nil {
        // best-effort: never block the human's allow on a persist failure
        // g.log(...) once a sink exists
    }
    ```

## Nits

### internal/gateway/gateway.go
- **L200** — dense doubly-nested `append` on one line
  - **Rule:** decisions.md §Line length — no hard cap, but "a line that is too long" hurts readability; prefer a clear intermediate.
  - **Found:** `policy = capability.Policy{Rules: append(append([]capability.Rule(nil), g.Policy.Rules...), extra...)}`
  - **Suggest:**
    ```go
    rules := append([]capability.Rule(nil), g.Policy.Rules...)
    rules = append(rules, extra...)
    policy = capability.Policy{Rules: rules}
    ```

- **L41** — very long single-line `Sprintf` in `Error()`. Intentional (message is model-facing guidance); no rule violated (lowercase, no trailing punctuation, correct `%q`). Leaving as-is is fine.

## Good
- `Do[T any]` as a free function (not a method) with the mandatory `EgressScan` value — makes "external effect ships unscanned" unrepresentable; the closure-based effect is the cleanest possible bypass-proof shape.
- `epochsOnce`/`epochRegistry()`: the "Scope can only be minted by `Guard.NewScope`" type-invariant replaces a former comment-enforced invariant, and the `sync.Once` publishes the registry under the same barrier every reader observes — race-free by design.
- `factLine` riding *beneath* a trusted semantic intent (never replacing it) so a template can't hide the real (capability, target) — correct, host-computed, guest-untrusted.
- `RateLimitedError.Unwrap() → ErrDenied` keeps `errors.Is(err, ErrDenied)` holding while carrying `RetryAfter`. Idiomatic error composition.
