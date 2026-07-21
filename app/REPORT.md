# Go review — app (main package: main.go, secrets.go, oauth.go)

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 1 minor · 3 nits

Tooling: `gofmt -l` clean · `go vet ./app/` clean · no banned identifiers ("capability"/"effect"/"axis"/"creds") found.

## Blockers
(none)

## Major
(none)

## Minor

### app/oauth.go
- **L59** — `runAuth` takes `log *slog.Logger` but never uses it
  - **Rule:** decisions.md §Receiver names: "Not an underscore; omit the name if it is unused" (same no-dead-parameter principle; also CLAUDE.md §6 "Kein Backward-Compat-Cruft"). `go vet` does not flag unused *function* params, so this hides.
  - **Found:** the sole caller (`main.go:53 runAuth(ctx, name, logger)`) constructs and passes a logger that `runAuth` discards; the body logs nothing (uses only `fmt`). Misleading signature + cruft.
  - **Suggest:** drop the param and the argument:
    ```go
    func runAuth(ctx context.Context, name string) error {
    // call site:
    if err := runAuth(ctx, name); err != nil {
    ```
    (or actually log via it, if operator-visible auth logging is intended).

## Nits

### app/main.go
- **L118** — `err != nil && err != http.ErrServerClosed`
  - **Rule:** best_practices.md §Error handling: "the error must be equal (in the sense of `==`) … If `process` returns wrapped errors … you can use `errors.Is`." Plain `!=` is adequate only if `serve.Serve` returns the sentinel unwrapped.
  - **Found:** if `serve.Serve` ever wraps its shutdown error (`%w`), this check misfires and reports a normal shutdown as fatal (`os.Exit(1)`).
  - **Suggest:** `if err := serve.Serve(...); err != nil && !errors.Is(err, http.ErrServerClosed) {`

- **L47, L80** — logger constructed twice with identical `slog.NewTextHandler(os.Stderr, …LevelInfo)`
  - **Found:** the `auth` branch (L47) and the main path (L80) duplicate the handler setup. Not a rule violation; a small `newLogger()` helper would remove the repeat.

### app/secrets.go, app/oauth.go
- **L30/L112 etc.** — parameter named `log` here vs `logger` in main.go
  - **Rule:** decisions.md §Naming (consistency); `log` also shadows the stdlib `log` package name (not imported here, so no conflict — readability smell only).
  - **Found:** `buildSecrets(log …)`, `loadBindings(…, log)`, `registerOAuth(…, log)`, `runAuth(…, log)` use `log`; `main.go` uses `logger`. Pick one across the package.

## Good
- Fail-closed vault handling: `buildSecrets`/`openVault` return `(nil, nil)` and the assistant runs credential-less — house style, clearly documented.
- Errors wrapped with `%w` at boundaries (`oauth.go:62,80`); sentinel `errors.New("wrong master passphrase")` lower-cased, no punctuation (decisions.md §Error strings).
- `signal.NotifyContext` + `defer stop()`, ctx threaded through `run`/`serve`/`StartAgents` — clean cancellation.
