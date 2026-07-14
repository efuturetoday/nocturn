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
	"math"
	"net/url"
	"regexp"
	"sort"
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
		if len(sc.scanExact(p)) > 0 {
			return fmt.Errorf("%w: a stored vault secret in the outbound request", ErrLeaked)
		}
		for _, text := range []string{p, percentDecode(p)} {
			for _, h := range sc.scanPatterns(text) {
				if h.action == actionBlock {
					return fmt.Errorf("%w: %s pattern in the outbound request", ErrLeaked, h.rule)
				}
			}
		}
	}
	return nil
}

// RedactIngress replaces every stored vault value and block/redact-action pattern
// in b with [REDACTED], so a secret echoed back in a response never reaches the
// model. warn-action patterns are left in place.
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
	return out
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

// applyRedactions replaces the given spans of text with [REDACTED]. Spans are
// sorted; any span starting inside a prior one is skipped (overlap-safe).
func applyRedactions(text string, spans [][2]int) string {
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var b strings.Builder
	last := 0
	for _, s := range spans {
		if s[0] < last {
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
