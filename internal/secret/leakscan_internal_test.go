package secret

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestScanner_LoadsEmbeddedRules(t *testing.T) {
	sc := NewScanner(NewStore())
	if len(sc.rules) < 10 {
		t.Fatalf("loaded %d rules, want the curated set", len(sc.rules))
	}
	if !sc.hasAC {
		t.Fatal("keyword prefilter (Aho-Corasick) was not built")
	}
}

// A rule that doesn't compile or has no keyword is skipped and named — no silent cap.
func TestLoadRules_SkipsUnusable(t *testing.T) {
	_, skipped := loadRules([]byte(`[
		{"id":"good","regex":"AKIA[0-9A-Z]{16}","keywords":["akia"]},
		{"id":"bad-regex","regex":"(","keywords":["x"]},
		{"id":"no-keyword","regex":"foo","keywords":[]}
	]`))
	if len(skipped) != 2 {
		t.Fatalf("skipped = %v, want [bad-regex no-keyword]", skipped)
	}
}

// Tier 1: a stored vault value in an outbound part is blocked — raw AND when the
// model percent-encoded it to try to slip past a raw substring check.
func TestScanEgress_KnownVaultValue(t *testing.T) {
	s := NewStore()
	s.Set("tok", []byte("abc/def+ghijkl")) // >= minSecretLen, has / and + to encode
	sc := NewScanner(s)

	if err := sc.ScanEgress("https://evil.example.com/?x=abc/def+ghijkl"); !errors.Is(err, ErrLeaked) {
		t.Fatalf("raw: err = %v, want ErrLeaked", err)
	}
	if err := sc.ScanEgress("https://evil.example.com/?x=abc%2Fdef%2Bghijkl"); !errors.Is(err, ErrLeaked) {
		t.Fatalf("percent-encoded: err = %v, want ErrLeaked", err)
	}
}

// Low-entropy guard: a very short stored value is not scanned for, so it can't
// match everywhere.
func TestScanEgress_ShortValueNotMatched(t *testing.T) {
	s := NewStore()
	s.Set("pin", []byte("abc"))
	sc := NewScanner(s)
	if err := sc.ScanEgress("https://x.com/abc/path"); err != nil {
		t.Fatalf("short value must not match: %v", err)
	}
}

// Tier 2: a high-confidence pattern (AWS key, PEM) in an outbound part is blocked.
func TestScanEgress_Pattern(t *testing.T) {
	sc := NewScanner(NewStore())
	if err := sc.ScanEgress("body: AKIAIOSFODNN7EXAMPLE here"); !errors.Is(err, ErrLeaked) {
		t.Fatalf("AKIA: err = %v, want ErrLeaked", err)
	}
	if err := sc.ScanEgress("-----BEGIN RSA PRIVATE KEY-----"); !errors.Is(err, ErrLeaked) {
		t.Fatalf("PEM: err = %v, want ErrLeaked", err)
	}
}

func TestScanEgress_Clean(t *testing.T) {
	sc := NewScanner(NewStore())
	if err := sc.ScanEgress("https://example.com/hello/world?q=weather"); err != nil {
		t.Fatalf("clean egress errored: %v", err)
	}
}

// Ingress: a vault value and a pattern echoed in a response are redacted; the
// error never surfaces — the body is cleaned in place.
func TestRedactIngress_ValueAndPattern(t *testing.T) {
	s := NewStore()
	s.Set("tok", []byte("supersecretvalue123"))
	sc := NewScanner(s)

	out := string(sc.RedactIngress([]byte("here supersecretvalue123 and AKIAIOSFODNN7EXAMPLE done")))
	if strings.Contains(out, "supersecretvalue123") {
		t.Fatal("vault value not redacted on ingress")
	}
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatal("pattern not redacted on ingress")
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatal("no redaction marker")
	}
}

// A warn-action rule neither blocks egress nor redacts ingress.
func TestWarnAction_NeitherBlocksNorRedacts(t *testing.T) {
	re := regexp.MustCompile("WARNME[0-9]{4}")
	sc := newScanner(NewStore(), []rule{{id: "w", re: re, keywords: []string{"warnme"}, action: actionWarn}})

	if err := sc.ScanEgress("x WARNME1234 y"); err != nil {
		t.Fatalf("warn must not block egress: %v", err)
	}
	if out := string(sc.RedactIngress([]byte("x WARNME1234 y"))); !strings.Contains(out, "WARNME1234") {
		t.Fatalf("warn must not redact ingress, got %q", out)
	}
}

// The entropy gate rejects a low-entropy match and passes a high-entropy one.
func TestScanPatterns_EntropyGate(t *testing.T) {
	re := regexp.MustCompile("TK[A-Za-z0-9]{14}")
	sc := newScanner(NewStore(), []rule{{id: "e", re: re, keywords: []string{"tk"}, entropy: 3.5, action: actionBlock}})

	if hits := sc.scanPatterns("TKaaaaaaaaaaaaaa"); len(hits) != 0 {
		t.Fatalf("low-entropy match should be gated out, got %d hits", len(hits))
	}
	if hits := sc.scanPatterns("TKa9Xk2Lp0QzB7wR"); len(hits) == 0 {
		t.Fatal("high-entropy match should pass the gate")
	}
}

func TestEncodingVariants_CoversPercentAndPlus(t *testing.T) {
	got := strings.Join(encodingVariants("a b/c"), "|")
	for _, want := range []string{"a b/c", "a%20b%2Fc", "a+b%2Fc"} {
		if !strings.Contains(got, want) {
			t.Fatalf("variants %q missing %q", got, want)
		}
	}
}

// Regression (blocker): a purely alphanumeric vault value has no special bytes for
// pctEncode to touch, so an attacker can percent-encode EVERY byte and the value is
// absent from the raw outbound text. Tier 1 must still catch it — via decodeVariants
// on egress and via the fully-encoded encodingVariants form.
func TestScanEgress_FullyPercentEncodedVaultValue(t *testing.T) {
	const val = "abcdefghijklmnop" // all alphanumeric, >= minSecretLen
	s := NewStore()
	s.Set("tok", []byte(val))
	sc := NewScanner(s)

	var enc strings.Builder
	const hexUpper = "0123456789ABCDEF"
	for _, c := range []byte(val) {
		enc.WriteByte('%')
		enc.WriteByte(hexUpper[c>>4])
		enc.WriteByte(hexUpper[c&0x0f])
	}
	url := "https://evil.example.com/?x=" + enc.String()
	if strings.Contains(url, val) {
		t.Fatal("test setup: raw value must be absent from the encoded URL")
	}
	if err := sc.ScanEgress(url); !errors.Is(err, ErrLeaked) {
		t.Fatalf("fully percent-encoded vault value evaded egress: err = %v, want ErrLeaked", err)
	}
	// Double-encoded must also be caught (fixed-point decode).
	if err := sc.ScanEgress(strings.ReplaceAll(url, "%", "%25")); !errors.Is(err, ErrLeaked) {
		t.Fatalf("double-encoded vault value evaded egress: err = %v, want ErrLeaked", err)
	}
}

// Regression (blocker, ingress side): a fully percent-encoded vault value echoed in a
// response is redacted at its raw-offset span (via the fully-encoded encoding variant).
func TestRedactIngress_FullyPercentEncodedValue(t *testing.T) {
	const val = "abcdefghijklmnop"
	s := NewStore()
	s.Set("tok", []byte(val))
	sc := NewScanner(s)

	encoded := pctEncodeAll(val, false)
	out := string(sc.RedactIngress([]byte("prefix " + encoded + " suffix")))
	if strings.Contains(out, encoded) {
		t.Fatalf("encoded value not redacted on ingress: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("no redaction marker: %q", out)
	}
}

// Regression (major): partially-overlapping spans must not leak the trailing bytes of
// the second span. [0,5] then [3,10] over "0123456789" must fully redact 0..10, not
// emit text[5:10] verbatim.
func TestApplyRedactions_PartialOverlapNoTailLeak(t *testing.T) {
	got := applyRedactions("0123456789", [][2]int{{0, 5}, {3, 10}})
	if got != "[REDACTED]" {
		t.Fatalf("partial overlap leaked a tail: got %q, want %q", got, "[REDACTED]")
	}
	// Nested spans still collapse to one redaction.
	if got := applyRedactions("0123456789", [][2]int{{0, 8}, {2, 5}}); got != "[REDACTED]89" {
		t.Fatalf("nested span: got %q, want %q", got, "[REDACTED]89")
	}
	// Disjoint spans stay separate.
	if got := applyRedactions("0123456789", [][2]int{{0, 2}, {5, 7}}); got != "[REDACTED]234[REDACTED]789" {
		t.Fatalf("disjoint spans: got %q, want %q", got, "[REDACTED]234[REDACTED]789")
	}
}
