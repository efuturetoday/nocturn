package secret

// leakscan.go is the bidirectional secret scanner. It sits at the network
// boundary and catches secrets crossing it in either direction:
//
//   - Egress (the guest-built request): a stored vault value, or a high-confidence
//     secret pattern, in an outbound URL/header/body is exfiltration → block.
//   - Ingress (the response): a stored vault value or secret pattern is redacted
//     before the body reaches the model, so an echoed secret never enters context.
//
// Two tiers. Tier 1 is the load-bearing one and false-positive free: an exact
// match against the actual vault values (encoding-robust, since the model could
// URL-encode a secret to evade a raw substring check). Tier 2 is defense in depth
// against secrets we do NOT hold: a curated gitleaks-derived ruleset (embedded as
// data, not the gitleaks library), gated by a keyword prefilter (Aho-Corasick) and
// a Shannon-entropy threshold, exactly as gitleaks does it.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"

	aho "github.com/petar-dambovaliev/aho-corasick"
)

// ErrLeaked is returned when an outbound request carries a secret. The error
// never contains the secret itself — only a masked hint or the rule id.
var ErrLeaked = errors.New("secret leak blocked")

//go:embed rules_gitleaks.json
var gitleaksRules []byte

// minSecretLen is the low-entropy guard for Tier 1: a stored value shorter than
// this is not scanned for, so a 3-character "secret" can't match everywhere.
const minSecretLen = 6

type leakAction int

const (
	actionBlock  leakAction = iota // outbound → ErrLeaked; inbound → redact
	actionRedact                   // inbound → redact; outbound → allow (breaking a URL is pointless)
	actionWarn                     // log only
)

func parseAction(s string) leakAction {
	switch s {
	case "redact":
		return actionRedact
	case "warn":
		return actionWarn
	default:
		return actionBlock
	}
}

type rule struct {
	id       string
	re       *regexp.Regexp
	keywords []string
	entropy  float64
	action   leakAction
}

type jsonRule struct {
	ID       string   `json:"id"`
	Regex    string   `json:"regex"`
	Keywords []string `json:"keywords"`
	Entropy  float64  `json:"entropy"`
	Action   string   `json:"action"`
}

// Scanner scans bytes crossing the network boundary for secrets. A nil *Scanner
// is a no-op, so callers can hold an optional one.
type Scanner struct {
	store     *Store
	rules     []rule
	ac        aho.AhoCorasick
	kwToRules [][]int // keyword (Aho-Corasick pattern index) -> rule indices
	hasAC     bool
	log       *slog.Logger // security-event trace; nil = silent (never logs a secret value)
}

// SetLogger attaches a logger for security events — a blocked egress (Warn) and an ingress
// redaction (Info). The caller passes a logger already tagged (e.g. component=secret, ws); the
// Scanner logs only rule ids / tiers / counts, never a secret value. nil disables it.
func (sc *Scanner) SetLogger(l *slog.Logger) {
	if sc != nil {
		sc.log = l
	}
}

// NewScanner builds a scanner over store with the embedded gitleaks ruleset.
func NewScanner(store *Store) *Scanner {
	rules, skipped := loadRules(gitleaksRules)
	for _, s := range skipped {
		log.Printf("leakscan: skipping rule %q (not usable): no silent cap", s)
	}
	return newScanner(store, rules)
}

// loadRules compiles the embedded ruleset. A rule whose regex does not compile
// (e.g. a non-RE2 feature) or that has no keyword to prefilter on is skipped and
// named in the returned slice — never silently dropped.
func loadRules(data []byte) (rules []rule, skipped []string) {
	var jr []jsonRule
	if err := json.Unmarshal(data, &jr); err != nil {
		return nil, []string{"<ruleset did not parse>"}
	}
	for _, r := range jr {
		re, err := regexp.Compile(r.Regex)
		if err != nil || len(r.Keywords) == 0 {
			skipped = append(skipped, r.ID)
			continue
		}
		kws := make([]string, len(r.Keywords))
		for i, k := range r.Keywords {
			kws[i] = strings.ToLower(k)
		}
		rules = append(rules, rule{id: r.ID, re: re, keywords: kws, entropy: r.Entropy, action: parseAction(r.Action)})
	}
	return rules, skipped
}

func newScanner(store *Store, rules []rule) *Scanner {
	kwRules := map[string][]int{}
	for ri, r := range rules {
		for _, kw := range r.keywords {
			kwRules[kw] = append(kwRules[kw], ri)
		}
	}
	kws := make([]string, 0, len(kwRules))
	kwToRules := make([][]int, 0, len(kwRules))
	for kw, ris := range kwRules {
		kws = append(kws, kw)
		kwToRules = append(kwToRules, ris)
	}
	sc := &Scanner{store: store, rules: rules, kwToRules: kwToRules}
	if len(kws) > 0 {
		builder := aho.NewAhoCorasickBuilder(aho.Opts{AsciiCaseInsensitive: true, MatchKind: aho.StandardMatch})
		sc.ac = builder.Build(kws)
		sc.hasAC = true
	}
	return sc
}

// ScanEgress reports a leak in any outbound part (URL, header values, body). A
// stored vault value (Tier 1) or a block-action pattern (Tier 2) — in the raw or
// percent-decoded text — yields ErrLeaked. The error never carries the secret.
func (sc *Scanner) ScanEgress(parts ...string) error {
	if sc == nil {
		return nil
	}
	for _, p := range parts {
		if p == "" {
			continue
		}
		// Scan the raw part AND every percent-decoding of it. Tier 1 (exact) must
		// run over the decoded text too: a secret URL-encoded to evade a raw
		// substring check — even every byte, even multiple layers — is recovered
		// by decodeVariants and then caught. Running only scanExact(p) here would
		// let a fully percent-encoded vault value slip past the load-bearing tier.
		for _, text := range decodeVariants(p) {
			if len(sc.scanExact(text)) > 0 {
				if sc.log != nil {
					sc.log.Warn("egress blocked", "tier", "vault") // a stored value tried to leave; never log the value
				}
				return fmt.Errorf("%w: a stored vault secret in the outbound request", ErrLeaked)
			}
			for _, h := range sc.scanPatterns(text) {
				if h.action == actionBlock {
					if sc.log != nil {
						sc.log.Warn("egress blocked", "tier", "pattern", "rule", h.rule)
					}
					return fmt.Errorf("%w: %s pattern in the outbound request", ErrLeaked, h.rule)
				}
			}
		}
	}
	return nil
}

// RedactIngress replaces every stored vault value and block/redact-action pattern
// in b with [REDACTED], so a secret echoed back in a response never reaches the
// model. warn-action patterns are left in place. A percent-encoded stored value is
// matched via encodingVariants (including the fully-encoded form) so its raw-offset
// span is redacted directly — no decode-then-offset-map needed.
func (sc *Scanner) RedactIngress(b []byte) []byte {
	if sc == nil || len(b) == 0 {
		return b
	}
	text := string(b)
	spans := sc.scanExact(text)
	for _, h := range sc.scanPatterns(text) {
		if h.action == actionBlock || h.action == actionRedact {
			spans = append(spans, [2]int{h.start, h.end})
		}
	}
	if len(spans) == 0 {
		return b
	}
	if sc.log != nil {
		sc.log.Info("ingress redacted", "spans", len(spans)) // a secret was echoed back; count only, never the value
	}
	return []byte(applyRedactions(text, spans))
}

// scanExact finds spans equal to a stored vault value or one of its encoding
// variants (raw + percent/plus, upper/lower hex). This is Tier 1.
func (sc *Scanner) scanExact(text string) [][2]int {
	var spans [][2]int
	for _, v := range sc.store.knownValues() {
		if len(v) < minSecretLen {
			continue
		}
		for _, variant := range encodingVariants(string(v)) {
			for from := 0; ; {
				i := strings.Index(text[from:], variant)
				if i < 0 {
					break
				}
				start := from + i
				spans = append(spans, [2]int{start, start + len(variant)})
				from = start + len(variant)
			}
		}
	}
	return spans
}

type patHit struct {
	start, end int
	action     leakAction
	rule       string
}

// scanPatterns runs Tier 2: a keyword prefilter (Aho-Corasick) selects candidate
// rules, each candidate's regex runs, and a match passes only if its Shannon
// entropy meets the rule's threshold.
func (sc *Scanner) scanPatterns(text string) []patHit {
	if !sc.hasAC {
		return nil
	}
	cand := map[int]bool{}
	iter := sc.ac.IterOverlapping(text)
	for m := iter.Next(); m != nil; m = iter.Next() {
		for _, ri := range sc.kwToRules[m.Pattern()] {
			cand[ri] = true
		}
	}
	var hits []patHit
	for ri := range cand {
		r := sc.rules[ri]
		for _, loc := range r.re.FindAllStringIndex(text, -1) {
			if r.entropy > 0 && shannon(text[loc[0]:loc[1]]) < r.entropy {
				continue
			}
			hits = append(hits, patHit{start: loc[0], end: loc[1], action: r.action, rule: r.id})
		}
	}
	return hits
}

func encodingVariants(s string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	add(s)
	add(pctEncode(s, "%20", false))
	add(pctEncode(s, "%20", true))
	add(pctEncode(s, "+", false))
	add(pctEncode(s, "+", true))
	// Fully percent-encoded forms (every byte, incl. alphanumerics). pctEncode
	// above leaves unreserved bytes literal, so it does NOT cover a value that was
	// encoded byte-for-byte to evade a raw match; these variants do, at raw offsets
	// (so ingress redaction lands on the encoded span without offset-mapping).
	add(pctEncodeAll(s, false))
	add(pctEncodeAll(s, true))
	return out
}

// pctEncodeAll percent-encodes every byte of s, including unreserved ones.
// lowerHex chooses %xx vs %XX. Unlike pctEncode this leaves nothing literal, so
// it matches a value an attacker encoded byte-for-byte.
func pctEncodeAll(s string, lowerHex bool) string {
	hexDigits := "0123456789ABCDEF"
	if lowerHex {
		hexDigits = "0123456789abcdef"
	}
	var b strings.Builder
	b.Grow(len(s) * 3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

// pctEncode percent-encodes every non-unreserved byte of s. space is what a
// space becomes ("%20" or "+"); lowerHex chooses %xx vs %XX.
func pctEncode(s, space string, lowerHex bool) string {
	hex := "0123456789ABCDEF"
	if lowerHex {
		hex = "0123456789abcdef"
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == ' ' && space == "+":
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}

func percentDecode(s string) string {
	if d, err := url.QueryUnescape(s); err == nil {
		return d
	}
	return s
}

// maxDecodePasses bounds how many percent-decoding layers decodeVariants unwinds,
// so a maliciously deeply-encoded payload can't drive unbounded work.
const maxDecodePasses = 4

// decodeVariants returns s plus each successive percent-decoding of it, up to a
// fixed point (or maxDecodePasses). This unwinds multi-layer encoding so a scan
// sees the plaintext a receiving server would decode to — not just the raw text.
func decodeVariants(s string) []string {
	out := []string{s}
	seen := map[string]bool{s: true}
	cur := s
	for range maxDecodePasses {
		d := percentDecode(cur)
		if seen[d] {
			break
		}
		seen[d] = true
		out = append(out, d)
		cur = d
	}
	return out
}

// applyRedactions replaces the given spans of text with [REDACTED]. Spans are
// sorted; any span starting inside a prior one is skipped (overlap-safe).
func applyRedactions(text string, spans [][2]int) string {
	slices.SortFunc(spans, func(x, y [2]int) int { return x[0] - y[0] })
	var b strings.Builder
	last := 0
	for _, s := range spans {
		if s[0] < last {
			// Overlapping span. "Skip" is only safe when this span is nested in the
			// prior one; for a PARTIAL overlap ([0,5] then [3,10]) skipping without
			// extending last would emit the tail (text[5:10]) verbatim — a leak.
			// Absorb the tail into the current redaction instead.
			if s[1] > last {
				last = s[1]
			}
			continue
		}
		b.WriteString(text[last:s[0]])
		b.WriteString("[REDACTED]")
		last = s[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// shannon is the Shannon entropy (bits/byte) of s, used to gate low-entropy
// pattern matches out.
func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]float64
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	var e float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := c / n
		e -= p * math.Log2(p)
	}
	return e
}
