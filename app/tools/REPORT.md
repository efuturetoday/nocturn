# Go review — app/tools (net.go, tools.go)

**Verdict:** needs changes
Findings: 0 blockers · 1 major · 0 minor · 1 nit

Tooling: `gofmt -l` clean · `go vet ./app/tools` clean.

## Major
### app/tools/tools.go
- **L4, L16** — Banned lexicon term "capabilities" in doc comments.
  - **Rule:** House style — words "capability", "effect", "axis", "creds" are banned; flag any occurrence.
  - **Found:** L4 package doc `// … Heavier capabilities that carry their own runtime — code.run`; L16 `Base` doc `// … It grows as capabilities land (file, notify, time, …).`
  - **Suggest:** reword, e.g. "Heavier tools that carry their own runtime …" / "It grows as tools land (file, notify, time, …)."

## Nits
### app/tools/tools.go
- **L4** — Doc prose writes `code.run` (dot) while the actual tool and `CodeRunTool` const (L49) use `code_run`. Align the comment to `code_run`. (Underscore tool names are correct per the agentkit constraint — this is a stale dot in prose only.)

## Good
- net.go:109–191 `do` sequences gate → egress leak-scan (before injection, so the host bearer isn't mistaken for a leak) → border injection → bounded read (`io.LimitReader(resp.Body, maxBody)`, 64 KiB) → ingress redaction; ordering and cap correct and documented.
- net.go:215–244 host-pattern matching kept with the owning tool; parent-domain heuristic explicitly flagged non-public-suffix + human-confirmed.
- Split `http_read`/`http_write` makes the chosen tool the read/write intent with server-side method allowlists.
