package secret

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func pctEncodeAllUpper(s string) string {
	var b strings.Builder
	for i := range len(s) {
		b.WriteString(fmt.Sprintf("%%%02X", s[i]))
	}
	return b.String()
}

// hasRule reports whether any hit carries the given rule id.
func hasRule(hits []patHit, id string) bool {
	for _, h := range hits {
		if h.rule == id {
			return true
		}
	}
	return false
}

func TestScanPatterns_LowEntropy_NotMatched(t *testing.T) {
	sc := NewScanner(NewStore())
	const rule = "generic-assigned-secret"

	// Low-entropy match: dominated by a single byte → gated out.
	if hits := sc.scanPatterns("password=aaaaaaaaaaaaaaaa"); hasRule(hits, rule) {
		t.Fatal("low-entropy value matched an entropy-gated rule")
	}
	// High-entropy match: passes the threshold.
	if hits := sc.scanPatterns("password=Ab3Cd6Ef9Gh2Ij5Kl8Mn1"); !hasRule(hits, rule) {
		t.Fatal("high-entropy value did not match the entropy-gated rule")
	}
}

// TestScanPatterns_KeywordPrefilter_Required builds a rule whose regex does NOT
// contain its keyword, so a regex-matching text without the keyword must be
// filtered out by the Aho-Corasick prefilter before the regex ever runs.
func TestScanPatterns_KeywordPrefilter_Required(t *testing.T) {
	r := rule{
		id:       "num16",
		re:       regexp.MustCompile(`[0-9]{16,}`),
		keywords: []string{"zzz"},
		entropy:  0,
		action:   actionBlock,
	}
	sc := newScanner(NewStore(), []rule{r})

	if hits := sc.scanPatterns("1234567890123456"); len(hits) != 0 {
		t.Fatalf("prefilter skipped: matched without the keyword: %v", hits)
	}
	if hits := sc.scanPatterns("zzz 1234567890123456"); !hasRule(hits, "num16") {
		t.Fatal("keyword present but rule not evaluated")
	}
	// Behavioral: egress blocks only when the keyword co-occurs with the match.
	if err := sc.ScanEgress("1234567890123456"); err != nil {
		t.Fatalf("egress blocked without keyword: %v", err)
	}
	if err := sc.ScanEgress("zzz1234567890123456"); !errors.Is(err, ErrLeaked) {
		t.Fatalf("egress not blocked with keyword+match: %v", err)
	}
}

func TestApplyRedactions_PartialOverlap_NoTailLeak(t *testing.T) {
	// Regression: [0,5] then [3,10] must not emit text[5:10] verbatim.
	text := "0123456789abc"
	got := applyRedactions(text, [][2]int{{0, 5}, {3, 10}})
	if strings.Contains(got, "56789") {
		t.Fatalf("partial-overlap tail leaked: %q", got)
	}
	if got != "[REDACTED]abc" {
		t.Fatalf("applyRedactions = %q, want %q", got, "[REDACTED]abc")
	}
}

func TestApplyRedactions_NestedSpan_Skipped(t *testing.T) {
	text := "0123456789abc"
	got := applyRedactions(text, [][2]int{{0, 10}, {3, 5}})
	if got != "[REDACTED]abc" {
		t.Fatalf("nested span not absorbed: %q", got)
	}
}

func TestDecodeVariants_BoundedPasses(t *testing.T) {
	const raw = "ABCDEF"
	enc := raw
	for range 6 { // encode more layers than the decoder will unwind
		enc = pctEncodeAllUpper(enc)
	}
	out := decodeVariants(enc)
	if len(out) != maxDecodePasses+1 {
		t.Fatalf("decodeVariants returned %d entries, want %d", len(out), maxDecodePasses+1)
	}
	if slices.Contains(out, raw) {
		t.Fatal("decodeVariants unwound past its bound to the plaintext")
	}
}

func TestLoadRules_BadRegexOrNoKeyword_SkippedNamed(t *testing.T) {
	data := []byte(`[
		{"id":"bad-regex","regex":"a(","keywords":["a"],"action":"block"},
		{"id":"no-keyword","regex":"abc","keywords":[],"action":"block"},
		{"id":"good","regex":"abc","keywords":["abc"],"action":"block"}
	]`)
	rules, skipped := loadRules(data)

	if !slices.Contains(skipped, "bad-regex") {
		t.Errorf("bad regex not reported as skipped: %v", skipped)
	}
	if !slices.Contains(skipped, "no-keyword") {
		t.Errorf("no-keyword rule not reported as skipped: %v", skipped)
	}
	if len(rules) != 1 || rules[0].id != "good" {
		t.Fatalf("only the good rule should load, got %d rules", len(rules))
	}

	// A ruleset that does not parse is named, never silently dropped.
	if _, s := loadRules([]byte("not json")); len(s) != 1 || !strings.Contains(s[0], "did not parse") {
		t.Fatalf("bad ruleset not named: %v", s)
	}
}

func TestShannon_KnownValues(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want float64
	}{
		{"empty", "", 0},
		{"single symbol repeated", "aaaaaaaa", 0},
		{"two equal symbols", "ab", 1},
		{"four distinct symbols", "abcd", 2},
		{"balanced binary", "00001111", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shannon(tc.in); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("shannon(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestScanExact_MultipleOccurrences_AllSpans(t *testing.T) {
	store := NewStore()
	store.Set("s", []byte("secretval"))
	sc := newScanner(store, nil)

	spans := sc.scanExact("secretval and secretval")
	if len(spans) != 2 {
		t.Fatalf("scanExact found %d spans, want 2", len(spans))
	}
	if spans[0] != [2]int{0, 9} || spans[1] != [2]int{14, 23} {
		t.Fatalf("unexpected spans: %v", spans)
	}
}

// TestNewScanner_EmptyRuleset_Tier1StillWorks: with no Tier-2 rules the exact
// (Tier 1) matcher still blocks a stored secret.
func TestNewScanner_EmptyRuleset_Tier1StillWorks(t *testing.T) {
	store := NewStore()
	store.Set("s", []byte("supersecretvalue"))
	sc := newScanner(store, nil)

	if sc.hasAC {
		t.Fatal("empty ruleset should leave the Aho-Corasick prefilter disabled")
	}
	if hits := sc.scanPatterns("password=whatever-high-entropy-Ab3Cd6"); hits != nil {
		t.Fatalf("Tier 2 ran with no rules: %v", hits)
	}
	if err := sc.ScanEgress("leak supersecretvalue here"); !errors.Is(err, ErrLeaked) {
		t.Fatalf("Tier 1 exact match failed with an empty ruleset: %v", err)
	}
}
