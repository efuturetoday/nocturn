# Go review — app/hitl (broker.go, push.go)

**Verdict:** ship with nits
Findings: 0 blockers · 0 major · 1 minor · 2 nits

Tooling: `gofmt -l` clean · `go vet ./app/hitl` clean. Banned words: none found. crypto/rand used correctly; token comparisons constant-time; Ask is leak-free.

## Minor
### app/hitl/push.go
- **L24** — exported method `LogPusher.Push` has no doc comment.
  - **Rule:** decisions.md §Doc comments: "All top-level exported names must have doc comments … full sentences that begin with the name of the object being described." (`Pusher.Push` is documented, but the method on `LogPusher` is not.)
  - **Suggest:** `// Push logs that a device would be woken; the placeholder delivers nothing.`

## Nits
### app/hitl/broker.go
- **L219** — unexported `newID` panics on crypto/rand failure but is undocumented, whereas the sibling `auth.newID` documents the same panic. decisions.md §Doc comments: "as should unexported type or function declarations with unobvious behavior or meaning."
- **L155** — `Ask` returns nil error on `<-ctx.Done()`. decisions.md §Returning errors (Tip): "A function that takes a context.Context argument should usually return an error so that the caller can determine if the context was cancelled." Deliberate fail-closed (cancel = deny) and the bool already carries the decision, so optional — noted only for divergence from the tip.

## Good
- Ask is fully synchronous (no goroutine spawned; resolve channel buffered(1); `Resolve` sends non-blocking with `default`) — leak-free per decisions.md §Goroutine lifetimes.
- `conclude` runs on `context.WithoutCancel` so a cancelled turn still clears its prompts.
