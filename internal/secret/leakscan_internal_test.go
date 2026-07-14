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
