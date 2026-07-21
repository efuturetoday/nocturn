# Go review — app/serve (serve, approval, chat, conn, join, mdns, pairing, presence, workspace)

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 0 minor · 3 nits

Tooling: `gofmt -l app/serve/` clean · `go vet ./app/serve` clean. Banned words (`capability`/`effect`/`axis`/`creds`): none found.

**Concurrency / lifetime sweep — no findings:**
- `hub` (serve.go) copies the conn set under the mutex, then `trySend`s outside it — correct lock discipline.
- `conn.active`/`activeID` are mutated only on the single serve/dispatch goroutine (activate ← chatSubmit/chatOpen ← chat ← serve read-loop; the teardown defer runs on that same goroutine) — no shared-state race.
- Every spawned goroutine has a documented exit: `writer` on `ctx.Done()`/write-error; `render` when the session's `Subscribe()` channel closes (each session is `Close()`d on the next `activate` or the serve defer); the `context.AfterFunc` hook via deferred `stop()`; mDNS via deferred `shutdown()`. Satisfies decisions.md §Goroutine lifetimes.
- ctx is threaded, never stored on the struct (documented on `send`) — satisfies decisions.md §Contexts.
- Loop-var capture in `Serve`'s `OnChatUpdate` closure (`name`) is safe under go 1.26 per-iteration semantics.

## Nits
### app/serve/mdns.go
- **L22, L35** — inconsistent error annotation in `advertiseMDNS`.
  - **Rule:** best_practices.md §Adding information to errors: "consider adding information that you have but that the caller and/or callee might not."
  - **Found:** `strconv.Atoi` is wrapped (`"mdns: bad port in %q: %w"`), but the sibling `net.SplitHostPort` (L22) and `zeroconf.Register` (L38) errors return raw. Raw is defensible (caller logs `"mdns advertise failed"`), but the mixed treatment in one short function reads as oversight. Pick one convention.

### app/serve/workspace.go
- **L32** — `sort.Slice` where `slices.SortFunc` is the modern idiom.
  - **Rule:** preference — not mandated by the four reference docs (Effective Go predates the `slices` package); modernization nit only.
  - **Suggest:** `slices.SortFunc(items, func(a, b WorkspaceInfo) int { return strings.Compare(a.Name, b.Name) })`.

### app/serve/serve.go
- **L71/L74/L80 (+ pairing.go L36)** — the `*hub` value is named `broadcast`, yielding `broadcast.broadcast(...)`; the same type is the `hub` field on `conn` and the `hub` param on `newConn`.
  - **Rule:** preference — no reference-doc rule; clarity nit. One concept carries two names (`broadcast` local/param vs `hub` field/type); naming the local `hub` would read uniformly.

## Good
- Doc comments on every exported type/func; wire-protocol invariants (id-addressed routing, `[]` vs `null`, 4401-after-upgrade, out-of-band ctx) captured where a reader needs them.
- Clean transport/domain split (conn.go plumbing, one file per domain); `send` (backpressure) vs `trySend` (drop-and-resync) is a well-reasoned distinction.
- Consistent receiver names (`c *conn`, `h *hub`); fail-closed dispatch defaults returning explicit `newError`.
