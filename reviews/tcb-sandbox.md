# Go review — internal/sandbox (full-package audit, committed code)

**Scope:** full package `internal/sandbox` — 2 non-test files (`sandbox.go` 134 LOC, `engine.go` 180 LOC; 314 total), 2 sibling test files skimmed for context. Reference precedence: guide > decisions > best_practices > effective_go.

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 1 minor · 3 nits

**Tooling (evidence):**
- `gofmt -l internal/sandbox/*.go` → clean (no output).
- `go vet ./internal/sandbox/...` → clean (no output).

The security-critical invariants this package exists to hold are all correct (see Good). The findings below are style-level only; none touches the isolation guarantees.

---

## Blockers
None.

## Major
None.

## Minor

### internal/sandbox/sandbox.go
- **L113** — `finish` takes its `context.Context` as the **last** parameter, not the first.
  - **Rule:** `decisions.md` §Contexts: *"When passed to a function or method, `context.Context` is always the first parameter."* The listed exceptions (HTTP handlers, streaming RPC, `TestXXX`, entrypoints) do not apply to an unexported helper.
  - **Found:** `func finish(stdout, stderr []byte, err error, runCtx context.Context) (Result, error)` — the run context is threaded in last, after the error. `finish` reads `runCtx.Err()` / `context.Cause(runCtx)` (L122–123), so it is genuinely a context-consuming function, just one that reads rather than propagates. The convention still applies.
  - **Suggest:**
    ```go
    func finish(runCtx context.Context, stdout, stderr []byte, err error) (Result, error) {
    ```
    and at the sole call site (engine.go:110):
    ```go
    return finish(runCtx, stdout.Bytes(), stderr.Bytes(), err)
    ```

## Nits

### internal/sandbox/engine.go
- **L124** — `type dispatchFn = func(context.Context, []byte) ([]byte, error)` is a **type alias** (`=`) used purely to abbreviate a signature.
  - **Rule:** `decisions.md` §Type aliases: *"Do not use type aliases merely to save typing a long type name."* Aliases are meant for gradual migration / re-export, not local shorthand.
  - **Found:** The alias is unexported and used only within this file for the map value type and the `ctx.Value` assertion. A plain **defined** type (`type dispatchFn func(...)`) would carry the same brevity without being an alias; assignment from `HostFunc.Fn`'s unnamed func type stays implicit either way. Low impact — purely a preference.
  - **Suggest:** drop the `=`:
    ```go
    type dispatchFn func(context.Context, []byte) ([]byte, error)
    ```

- **L44 / L81** — mild parameter-name inconsistency for the two config structs: `NewEngine(..., ec EngineConfig)` vs `Run(..., cfg Config)` (and package-level `Run(..., cfg Config)`). No normative rule mandates a single spelling, but `cfg` is used for `Config` everywhere else; `ec` is a one-off. Purely cosmetic.

### internal/sandbox/sandbox.go
- **L53** — doc comment on `Config.Timeout` reads `// wall-clock CPU bound (0 = default 5s)`. "wall-clock" and "CPU" name two different budgets; the enforcement (`WithCloseOnContextDone` + `deadline.WithBudget`) is wall-clock, matching the rest of the package's prose ("wall-clock deadline"). Consider dropping "CPU" to avoid implying CPU-time accounting. Documentation nit only.

*(Skimmed for context — test files, not primary scope:)* the tests seed contexts with `context.Background()` throughout. `decisions.md` §Contexts Note (Go 1.24+): *"In tests, prefer using `(testing.TB).Context()` over `context.Background()` to provide the initial `context.Context` used by the test."* Optional modernization; not required.

---

## Good (idiomatic / security-correct — called out because this is the TCB core)

- **The wazero `Memory.Read` view is copied out immediately** (engine.go:161): `resp, err := fn(ctx, append([]byte(nil), view...))`. The transient view from `mod.Memory().Read` (L149) is never retained past the host call — exactly the project's documented pitfall (a `memory.grow` realloc would invalidate it). Correct.

- **Zero ambient authority is enforced by construction.** The `Config`/`EngineConfig` zero values grant nothing; the host module is only built `if len(ec.HostNames) > 0` (engine.go:57), and `CompileModule` not resolving imports means an ungranted import fails at `InstantiateModule` (engine.go:41–43, proven by `TestRun_UngrantedImport_CannotInstantiate` / `TestEngine_UnregisteredImport_CannotInstantiate`). Filesystem is opt-in per-run via `WithDirMount` only (sandbox.go:84–86), with `""` = no preopen (proven by `TestRun_NoWorkspace_HasNoFilesystem`).

- **One-instance-one-goroutine safety is honored and the shared state is provably read-only.** `e.hostNames` is written once in `NewEngine` then only read; each `Run` builds a fresh per-call dispatcher map and stamps it on the run ctx via the unexported `hostsKey struct{}` (engine.go:120–135). This is the sanctioned `context.Context` use — request-scoped data transiting an API (wazero) the package does not control — matching `decisions.md` §Contexts intent, and `TestEngine_ConcurrentDispatchersNeverCross` pins it under `-race`.

- **Fail-closed at every seam, never a silent allow:** unknown-but-registered import → guest-visible `error: host function "..." not granted` string (engine.go:157–159, nil-map read is safe); a dispatcher with no trampoline → loud `Run` rejection (engine.go:82–89). Matches the project's explicit-over-implicit, deny-by-default principle.

- **Deadline handling is precise.** The trap surfaces the context *cause* (`context.Cause`) rather than the opaque wazero `ExitError` trap code (sandbox.go:118–124), distinguishing `DeadlineExceeded` from `Canceled`; a normal WASI `exit(0)` is unwrapped to a nil error via `errors.As` (sandbox.go:126–128). `mod.Close` uses the **original** `ctx`, not the (possibly cancelled) `runCtx`, so instance teardown still runs after a trap (engine.go:106–108) — verified by `TestEngine_DeadlineTrapsRunawayGuest` (reuse after trap) and `TestEngine_ReusedAcrossManyRuns`.

- **Error wrapping and strings are idiomatic:** consistent `sandbox:` prefix, `%w` for wraps, `%q`/`%d` verbs, lowercase non-punctuated messages (sandbox.go:123/130/132, engine.go:65/72/87). Package doc comment and all exported identifiers are documented; receiver naming (`e *Engine`) is consistent.
