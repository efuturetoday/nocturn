package mail

import (
	"strings"
	"testing"
	"time"
)

// TestComposeRendersAnRFC5322Message pins the wire form of an outgoing message. It is a pure function
// of its inputs — the timestamp and the id are parameters for exactly this reason — so the whole
// header block can be compared rather than sampled.
func TestComposeRendersAnRFC5322Message(t *testing.T) {
	at := time.Date(2026, time.August, 15, 9, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	got, err := compose("ich@firma.de", Outgoing{
		To:      []string{"chef@firma.de", "buero@firma.de"},
		Subject: "Freitag",
		Body:    "Bis dann.",
	}, at, "abc@firma.de")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	want := "From: ich@firma.de\r\n" +
		"To: chef@firma.de, buero@firma.de\r\n" +
		"Subject: Freitag\r\n" +
		"Date: Sat, 15 Aug 2026 09:30:00 +0200\r\n" +
		"Message-ID: <abc@firma.de>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"Bis dann."
	if string(got) != want {
		t.Errorf("compose produced:\n%q\nwant:\n%q", got, want)
	}
}

// TestComposeEncodesNonASCII pins the two encodings that make an umlaut survive a trip through
// servers older than the assistant sending it: the subject as an RFC 2047 encoded word, the body as
// quoted-printable. Sending raw UTF-8 in a header is the mistake this guards.
func TestComposeEncodesNonASCII(t *testing.T) {
	got, err := compose("ich@firma.de", Outgoing{
		To:      []string{"chef@firma.de"},
		Subject: "Grüße",
		Body:    "Schöne Grüße",
	}, time.Now(), "id@firma.de")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	text := string(got)
	subject := line(text, "Subject: ")
	if strings.Contains(subject, "Grüße") {
		t.Errorf("the subject went out as raw UTF-8: %q", subject)
	}
	if !strings.HasPrefix(subject, "=?utf-8?") {
		t.Errorf("the subject is not an encoded word: %q", subject)
	}
	body := text[strings.Index(text, "\r\n\r\n")+4:]
	if strings.Contains(body, "ö") {
		t.Errorf("the body was not quoted-printable encoded: %q", body)
	}
	if !strings.Contains(body, "=C3=B6") {
		t.Errorf("the body lost its umlaut instead of encoding it: %q", body)
	}
}

// TestMessageIDIsUnique pins that two identical messages are two messages. A content-derived id would
// let a server deduplicate this week's reminder against last week's.
func TestMessageIDIsUnique(t *testing.T) {
	a, b := messageID("ich@firma.de"), messageID("ich@firma.de")
	if a == b {
		t.Fatalf("two message ids were identical: %q", a)
	}
	for _, id := range []string{a, b} {
		if !strings.HasSuffix(id, "@firma.de") {
			t.Errorf("message id %q is not anchored to the sender's domain", id)
		}
	}
}

// TestMessageIDWithoutADomain pins that a malformed sender still yields a well-formed id rather than
// one ending in a bare "@".
func TestMessageIDWithoutADomain(t *testing.T) {
	if id := messageID("ich"); !strings.HasSuffix(id, "@localhost") {
		t.Errorf("message id = %q, want a localhost fallback", id)
	}
}

// line returns the rest of the line starting with prefix.
func line(text, prefix string) string {
	for l := range strings.SplitSeq(text, "\r\n") {
		if after, ok := strings.CutPrefix(l, prefix); ok {
			return after
		}
	}
	return ""
}
