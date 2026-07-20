# Go review — internal/secret (full-package audit, committed files on disk)

**Scope:** all non-test `.go` in `internal/secret/` — `store.go` (86), `vault.go` (233), `master.go` (155), `credential.go` (272), `leakscan.go` (330). Test siblings skimmed for context. `migrate.go` is absent on disk (deleted) — not reviewed. Precedence: guide > decisions > best_practices > effective_go.

**Verdict:** needs changes
Findings: **1 blocker** · **1 major** · **3 minor** · **2 nits**

Tooling: `gofmt -l` clean · `go vet ./internal/secret/...` clean. Crypto uses `crypto/rand` throughout (vault nonce `vault.go:181`, master salt `master.go:122`) — **no `math/rand`**, so the crypto/rand blocker criterion does not fire. AES-256-GCM sealing, scrypt→HKDF key hierarchy, GCM-tag fail-closed, and the guest presence-only boundary are all sound. The findings below are in the leak scanner (soundness gaps) plus style.

---

## Blockers

### internal/secret/leakscan.go
- **L146–166 (`ScanEgress`)** — Tier-1 exact scan is not run over the percent-decoded text, so a fully percent-encoded known vault secret evades egress blocking.
  - **Rule (bug / security issue — Severity rubric):** "**Blocker** — bug, data race, leak, security issue, broken public API, panic on user input." The file's own contract (leakscan.go:13-15) states Tier 1 is "the load-bearing one … encoding-robust, since the model could URL-encode a secret to evade a raw substring check."
  - **Found:** `scanExact(p)` is called only on the raw part `p` (L154). The decode loop `for _, text := range []string{p, percentDecode(p)}` (L157) runs **only `scanPatterns`** (Tier 2). Tier 1 relies on `encodingVariants` (L244) to cover encodings, but `pctEncode` (L263-283) leaves every *unreserved* byte — crucially **all alphanumerics** — literal. So for a secret like `ghp_pat123` (or any base64/hex token), `encodingVariants` yields essentially just the raw string. An attacker/injection that percent-encodes **every** byte (`%67%68%70%5f…`) produces text where the raw value is absent, `scanExact(p)` finds nothing, and `scanExact` is never applied to `percentDecode(p)` (which would recover the plaintext). The known-vault-value exfiltration guard — the load-bearing tier — is bypassed. Tier 2 does not compensate: a generic vault value matches no gitleaks pattern/keyword. (`RedactIngress` at L177 has the same asymmetry: `scanExact(text)` runs on the raw body only.)
  - **Suggest:** fold `scanExact` into the same decode loop as `scanPatterns`, mirroring the two already do for Tier 2:
    ```go
    for _, p := range parts {
        if p == "" {
            continue
        }
        for _, text := range []string{p, percentDecode(p)} {
            if len(sc.scanExact(text)) > 0 {
                return fmt.Errorf("%w: a stored vault secret in the outbound request", ErrLeaked)
            }
            for _, h := range sc.scanPatterns(text) {
                if h.action == actionBlock {
                    return fmt.Errorf("%w: %s pattern in the outbound request", ErrLeaked, h.rule)
                }
            }
        }
    }
    ```
    Apply the analogous fix in `RedactIngress` (also scan `percentDecode(text)` and offset-map, or at minimum document that ingress bodies are assumed un-encoded). Add a regression test: a purely alphanumeric vault value, every byte percent-encoded, must yield `ErrLeaked`.

---

## Major

### internal/secret/leakscan.go
- **L294–308 (`applyRedactions`)** — partially-overlapping spans leak the tail of a secret.
  - **Rule (bug — Severity rubric):** "**Blocker/Major** — bug … leak." The doc comment claims "any span starting inside a prior one is skipped (overlap-safe)" — but *skip* is only safe when the later span is **nested**, not when it **partially overlaps**.
  - **Found:** spans are sorted by start; a span with `s[0] < last` is `continue`d without extending `last`. For two partially overlapping secret spans — e.g. `[0,5]` then `[3,10]` (a vault value ending mid-token and a gitleaks pattern starting inside it and extending past) — the second is dropped and `last` stays `5`, so the trailing `text[5:10]` (the second secret's tail) is emitted verbatim by the final `b.WriteString(text[last:])`. A redactor must **merge**, not skip. Reachable when a Tier-1 exact span and a Tier-2 pattern span (or two exact-variant spans) genuinely partially overlap.
  - **Suggest:** on the skip branch, still extend the redaction to cover the overlap tail:
    ```go
    for _, s := range spans {
        if s[0] < last {
            if s[1] > last {
                last = s[1] // absorb the overlapping tail into the current redaction
            }
            continue
        }
        b.WriteString(text[last:s[0]])
        b.WriteString("[REDACTED]")
        last = s[1]
    }
    b.WriteString(text[last:])
    ```

---

## Minor

### internal/secret/master.go
- **L77–84 (`subKey`), L93–99 (`Verifier`)** — panics propagate across the package's public API boundary.
  - **Rule:** `best_practices.md` §When to panic: "The key attribute of this design is that these **panics are never allowed to escape across package boundaries** and do not form part of the package's API." `decisions.md` §Don't panic: "For errors that indicate 'impossible' conditions … a function may reasonably return an error or call `log.Fatal`."
  - **Found:** `subKey` (called by exported `WorkspaceKey`, `Verifier`, `CheckVerifier`) `panic`s on an HKDF error; `Verifier` `panic`s on a seal error. Both conditions are genuinely impossible (32-byte HKDF ≪ 255·HashLen; AES-GCM over 26 bytes), and the code documents that — so this is a defensible invariant check in the spirit of the stdlib. But because the panicking functions are reachable through exported methods with no `recover` at the boundary, a panic *would* escape as the package's behavior, which the cited rule advises against. Acceptable as-is; if tightened, either keep the invariant panic (it's provably unreachable) or thread an error through `WorkspaceKey`. Flagging for the record, not blocking.

- **L44–46 (`MasterWorkFactor`)** — functional-option name departs from the project's own `With*` convention.
  - **Rule:** project convention (CLAUDE.md §6: "Funktionale Options … `ntfy.WithAuth`, `RateLimiter.WithLimit`"). `decisions.md`/`best_practices.md` do not mandate a `With` prefix, so this is a consistency point, not a normative-Google rule.
  - **Found:** the option constructor is `MasterWorkFactor(logN int) MasterOption`, while the rest of the codebase names options `WithX`. It also reads awkwardly against the package const `masterWorkFactor`. Consider `WithWorkFactor` (or `WithMasterWorkFactor`) for consistency.

### internal/secret/leakscan.go
- **L157 / L285–290 (`percentDecode`)** — single-pass decode; double-encoding is not unwound.
  - **Rule:** defense-in-depth consistency with the file's stated threat model (leakscan.go:13-15). Style docs are silent, so this is a recommendation.
  - **Found:** `ScanEgress` decodes once. Combined with the Blocker fix above this is much less severe, but a doubly percent-encoded payload still only gets one `url.QueryUnescape`. Consider decoding to a fixed point (loop until stable or a small bounded number of passes) before Tier-1/Tier-2 scanning. Minor because Tier-1-over-decoded (the Blocker patch) already closes the common single-encode case.

---

## Nits

### internal/secret/leakscan.go
- **L93 (`log.Printf` in `NewScanner`)** — a TCB library constructor writes skipped-rule diagnostics to the process-global logger as a side effect. `decisions.md` §Logging only governs *which* log package, and there is no citable rule forbidding library logging, so this is a preference. `loadRules` already *returns* `skipped`; consider surfacing it to the caller (or an injected logger) rather than emitting to stderr from inside the package. Not blocking.
- **L295 (`sort.Slice`)** — modernizable to `slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })` (Go 1.21+); purely stylistic, style docs silent.

---

## Good (≤5)
- **Guest boundary is type-enforced:** `Store.value`/`snapshot`/`knownValues` are unexported; `GuestView` exposes only `Exists`; `var _ GuestView = (*Store)(nil)` (store.go:86) proves it at compile time. The value bytes are unreachable through any guest-held surface. Exemplary.
- **Fail-closed crypto framing:** GCM-tag mismatch maps to `ErrWrongPassphrase` (vault.go:215), magic + format byte + version-binding AAD reject cross-version confusion, oversized ciphertext/plaintext capped (vault.go:94, 217), unknown JSON fields rejected (`DisallowUnknownFields`). Missing file → seal-empty-immediately so a wrong key can never clobber a real vault.
- **Correct concurrency in `Injector.InjectMatching`** (credential.go:173-209): matching pairs snapshotted under the lock, `Resolver.Value` (potential refresh I/O) invoked *outside* it — no lock held across I/O, no data race; any resolver error is fail-closed (no half-authenticated request).
- **Explicit fail-closed matchers:** `capMatches`/`hostMatches`/`ownerMatches` (credential.go:242-272) make `""` and bare `"*"` match *nothing*, with table-driven tests (`credential_internal_test.go`) covering suffix-confusion (`notexample.com`, `evil.example.com.attacker.io`).
- **Key hierarchy** (master.go): scrypt (2^18, r=8, p=1) for the low-entropy passphrase, HKDF-SHA256 domain-separated per workspace, non-secret salt/logN/verifier persisted, verifier lets a typo be caught before any vault is touched. Clean, well-documented, well-tested (`master_test.go` covers determinism, domain separation, verifier, e2e).
