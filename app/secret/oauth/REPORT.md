# Go review — app/secret/oauth (oauth.go, source.go)

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 1 minor · 2 nits

Tooling: `gofmt -l` clean · `go vet ./app/secret/oauth/` clean · no banned identifiers ("capability"/"effect"/"axis"/"creds") found.

## Blockers
(none)

## Major
(none)

## Minor

### app/secret/oauth/oauth.go
- **L78, L83, L89, L93** — callback handler does a *blocking* send on `results` (buffer 1); a second `/callback` hit hangs the handler, and the deferred `srv.Shutdown(context.Background())` then blocks forever
  - **Rule:** decisions.md §Goroutine lifetimes: "Goroutines can leak by blocking on channel sends or receives. The garbage collector will not terminate a goroutine blocked on a channel…"
  - **Found:** `results` has capacity 1. The first callback fills it and `Authorize` returns → deferred `Shutdown` (background ctx, no timeout) waits for in-flight handlers. If a second request reaches `/callback` before return (tab refresh, double redirect, or a probe), its handler blocks on `results <-` and never returns → `Shutdown` hangs indefinitely, so `Authorize` never returns. `render` in main.go already uses the correct non-blocking idiom.
  - **Suggest:** make the send non-blocking (first result wins):
    ```go
    select {
    case results <- result{code: code}:
    default:
    }
    ```
    applied at each `results <- …` site.

## Nits

### app/secret/oauth/source.go
- **L51-58** — `Value` holds `s.mu` while invoking `s.onChange(tok)`, which persists the token to the vault (disk I/O) under lock
  - **Found:** the mutex only guards `last`; running the persistence callback under it serializes all `Value` callers behind a disk write. Deliberate (ensures onChange fires once per change), so a trade-off, not a defect — but capturing `tok`/clearing the change flag under lock and calling `onChange` after `Unlock` avoids blocking readers during I/O.

### app/secret/oauth/oauth.go
- **L53** — default prompt: `fmt.Println("Open this URL to authorize:\n" + u)`
  - **Found:** string concatenation with an embedded `\n` inside `Println`; `fmt.Printf("Open this URL to authorize:\n%s\n", u)` reads cleaner. Purely stylistic.

## Good
- `net.Listen("tcp", "127.0.0.1:0")` — loopback-only inbound socket, closed on return; state (`randomState`, `crypto/rand`) + PKCE S256 guard the callback (decisions.md §crypto/rand satisfied).
- `NewSource` deliberately gives refresh I/O its own background ctx + timeout so a single request's cancellation can't kill the shared token; documented on both `NewSource` and `Value`.
- Errors consistently wrapped with `%w` and lower-cased, package-prefixed strings ("oauth: …") (decisions.md §Error strings).
- Package doc comment states the security invariant (guest never sees a token; structural `secret.Resolver` satisfaction, no import cycle).
