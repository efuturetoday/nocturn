package secret_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/app/secret"
)

// pctEncodeAll percent-encodes every byte of s (uppercase hex), matching the
// fully-encoded evasion form the scanner is built to recover.
func pctEncodeAll(s string) string {
	var b strings.Builder
	for i := range len(s) {
		b.WriteString(fmt.Sprintf("%%%02X", s[i]))
	}
	return b.String()
}

func TestScanEgress_StoredSecretRaw_Blocked(t *testing.T) {
	const val = "supersecretvalue"
	store := secret.NewStore()
	store.Set("s", []byte(val))
	sc := secret.NewScanner(store)

	err := sc.ScanEgress("https://evil.example.com/?leak=" + val)
	if !errors.Is(err, secret.ErrLeaked) {
		t.Fatalf("raw stored secret: got %v, want ErrLeaked", err)
	}
	if strings.Contains(err.Error(), val) {
		t.Fatalf("error string leaked the secret bytes: %q", err)
	}
}

func TestScanEgress_PercentEncodedSecret_Blocked(t *testing.T) {
	const val = "s3cr#t/v@lue!x"
	store := secret.NewStore()
	store.Set("s", []byte(val))
	sc := secret.NewScanner(store)

	if err := sc.ScanEgress("https://evil.example.com/?q=" + pctEncodeAll(val)); !errors.Is(err, secret.ErrLeaked) {
		t.Fatalf("percent-encoded secret: got %v, want ErrLeaked", err)
	}
}

func TestScanEgress_MultiLayerEncoded_Blocked(t *testing.T) {
	const val = "s3cr#t/v@lue!x"
	store := secret.NewStore()
	store.Set("s", []byte(val))
	sc := secret.NewScanner(store)

	// Two layers of percent-encoding — within maxDecodePasses.
	doubled := pctEncodeAll(pctEncodeAll(val))
	if err := sc.ScanEgress("https://evil.example.com/?q=" + doubled); !errors.Is(err, secret.ErrLeaked) {
		t.Fatalf("multi-layer encoded secret: got %v, want ErrLeaked", err)
	}
}

func TestRedactIngress_StoredSecret_Redacted(t *testing.T) {
	const val = "supersecretvalue"
	store := secret.NewStore()
	store.Set("s", []byte(val))
	sc := secret.NewScanner(store)

	out := string(sc.RedactIngress([]byte("the token is " + val + " ok")))
	if strings.Contains(out, val) {
		t.Fatalf("secret survived redaction: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("no redaction marker in output: %q", out)
	}
}

func TestRedactIngress_EncodedSecret_RedactedAtRawOffset(t *testing.T) {
	const val = "supersecretvalue"
	store := secret.NewStore()
	store.Set("s", []byte(val))
	sc := secret.NewScanner(store)

	encoded := pctEncodeAll(val)
	body := "prefix " + encoded + " suffix"
	out := string(sc.RedactIngress([]byte(body)))
	if strings.Contains(out, encoded) {
		t.Fatalf("encoded secret survived redaction: %q", out)
	}
	if strings.Contains(out, val) || !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("encoded span not redacted at its raw offset: %q", out)
	}
}

func TestScanEgress_ShortValue_NotScanned(t *testing.T) {
	// A stored value shorter than minSecretLen (6) must not be scanned for.
	store := secret.NewStore()
	store.Set("s", []byte("abcde")) // 5 bytes
	sc := secret.NewScanner(store)

	if err := sc.ScanEgress("value is abcde here"); err != nil {
		t.Fatalf("short value was scanned: %v", err)
	}
}

func TestNilScanner_Egress_NoError_Ingress_Passthrough(t *testing.T) {
	var sc *secret.Scanner
	if err := sc.ScanEgress("anything with a secret"); err != nil {
		t.Fatalf("nil scanner egress: %v", err)
	}
	in := []byte("unchanged body")
	if out := sc.RedactIngress(in); string(out) != string(in) {
		t.Fatalf("nil scanner mutated ingress: %q", out)
	}
}

func TestScanPatterns_BlockAction_Egress_Blocked(t *testing.T) {
	sc := secret.NewScanner(secret.NewStore()) // empty store: Tier 2 only
	const key = "sk-proj-abcdefghijklmnopqrstuvwxyz012345"

	err := sc.ScanEgress("Authorization: Bearer " + key)
	if !errors.Is(err, secret.ErrLeaked) {
		t.Fatalf("openai key egress: got %v, want ErrLeaked", err)
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("error string leaked the key value: %q", err)
	}
	if !strings.Contains(err.Error(), "openai-api-key") {
		t.Fatalf("error string missing the rule id: %q", err)
	}
}

func TestRedactIngress_RedactAction_RedactedButEgressAllowed(t *testing.T) {
	sc := secret.NewScanner(secret.NewStore())
	// A JWT — the ruleset marks it redact (not block).
	const jwt = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghij1234567890"

	// Egress: redact-action does not block (breaking a URL is pointless).
	if err := sc.ScanEgress("token=" + jwt); err != nil {
		t.Fatalf("redact-action blocked egress: %v", err)
	}
	// Ingress: redacted.
	out := string(sc.RedactIngress([]byte("resp " + jwt + " end")))
	if strings.Contains(out, jwt) || !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("redact-action pattern not redacted on ingress: %q", out)
	}
}

func TestScanPatterns_WarnAction_LoggedNotRedactedNotBlocked(t *testing.T) {
	sc := secret.NewScanner(secret.NewStore())
	// generic-assigned-secret is a warn rule (entropy-gated); high entropy value.
	const line = "password=Ab3Cd6Ef9Gh2Ij5Kl8Mn1"

	// warn does not block egress.
	if err := sc.ScanEgress(line); err != nil {
		t.Fatalf("warn-action blocked egress: %v", err)
	}
	// warn does not redact ingress — the value stays put.
	out := string(sc.RedactIngress([]byte(line)))
	if out != line {
		t.Fatalf("warn-action redacted ingress: %q", out)
	}
}
