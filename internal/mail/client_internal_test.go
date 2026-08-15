package mail

import (
	"strings"
	"testing"
)

// The body parser is the part of reading mail that is ours rather than the library's, and it is the
// part real mailboxes break: encoded words, transfer encodings, charsets that are not UTF-8, and the
// multipart tree a message hides its readable half in. So it is tested directly, on raw bytes, with
// no server involved.

// crlf turns a readable literal into what a mail actually is on the wire. A message with bare LF
// header separators parses differently (or not at all), so the tests must not accidentally rely on
// Go source formatting.
func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

func TestPlainTextSimpleMessage(t *testing.T) {
	raw := crlf(`From: chef@firma.de
To: ich@firma.de
Subject: Freitag
Content-Type: text/plain; charset=utf-8

Bis Freitag dann.
`)
	got, err := plainText(raw)
	if err != nil {
		t.Fatalf("plainText: %v", err)
	}
	if want := "Bis Freitag dann.\r\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestPlainTextPrefersThePlainPart pins that an alternative message yields its readable half and not
// its markup: a model handed the HTML branch would spend its context on style attributes.
func TestPlainTextPrefersThePlainPart(t *testing.T) {
	raw := crlf(`From: chef@firma.de
Subject: Freitag
Content-Type: multipart/alternative; boundary=xx

--xx
Content-Type: text/plain; charset=utf-8

Bis Freitag.
--xx
Content-Type: text/html; charset=utf-8

<html><body><p>Bis Freitag.</p></body></html>
--xx--
`)
	got, err := plainText(raw)
	if err != nil {
		t.Fatalf("plainText: %v", err)
	}
	if strings.Contains(got, "<html>") {
		t.Fatalf("HTML leaked into the text body: %q", got)
	}
	if !strings.Contains(got, "Bis Freitag.") {
		t.Errorf("plain part missing: %q", got)
	}
}

// TestPlainTextDecodesCharsetAndTransferEncoding is the case go-message was taken into the
// dependency graph for: a quoted-printable body in ISO-8859-1, which is what a German mail from a
// decade-old client still looks like.
func TestPlainTextDecodesCharsetAndTransferEncoding(t *testing.T) {
	raw := crlf(`From: chef@firma.de
Subject: Gruesse
Content-Type: text/plain; charset=iso-8859-1
Content-Transfer-Encoding: quoted-printable

Sch=F6ne Gr=FC=DFe
`)
	got, err := plainText(raw)
	if err != nil {
		t.Fatalf("plainText: %v", err)
	}
	if !strings.Contains(got, "Schöne Grüße") {
		t.Errorf("charset or transfer encoding not decoded: %q", got)
	}
}

// TestPlainTextHTMLOnlyIsEmptyNotAnError pins the deliberate choice: a message with no plain part is
// ordinary, not a failure. Returning an error would make a newsletter look like a broken mailbox.
func TestPlainTextHTMLOnlyIsEmptyNotAnError(t *testing.T) {
	raw := crlf(`From: newsletter@firma.de
Subject: Angebote
Content-Type: text/html; charset=utf-8

<html><body>Angebote</body></html>
`)
	got, err := plainText(raw)
	if err != nil {
		t.Fatalf("plainText: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want an empty body", got)
	}
}

// TestPlainTextSkipsAttachments pins that an attached file's bytes never become body text. A
// text/plain attachment is the case a content-type check alone would get wrong.
func TestPlainTextSkipsAttachments(t *testing.T) {
	raw := crlf(`From: chef@firma.de
Subject: Anhang
Content-Type: multipart/mixed; boundary=xx

--xx
Content-Type: text/plain; charset=utf-8

Siehe Anhang.
--xx
Content-Type: text/plain; charset=utf-8
Content-Disposition: attachment; filename="liste.txt"

GEHEIMER ANHANG
--xx--
`)
	got, err := plainText(raw)
	if err != nil {
		t.Fatalf("plainText: %v", err)
	}
	if strings.Contains(got, "GEHEIMER ANHANG") {
		t.Errorf("attachment body leaked into the message text: %q", got)
	}
	if !strings.Contains(got, "Siehe Anhang.") {
		t.Errorf("inline part missing: %q", got)
	}
}

func TestPlainTextEmpty(t *testing.T) {
	got, err := plainText(nil)
	if err != nil {
		t.Fatalf("plainText: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
