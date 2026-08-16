package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// This file is the SENDING side. It is short and it is the dangerous half: everything here leaves the
// household and cannot be taken back, which is why the gate sits in front of it (see tools.go) rather
// than inside.

// Vault entry names. Two entries rather than one blob, so the leak scanner knows each value on its
// own — a JSON blob holding both would register as a single secret and let the bare password through.
// The USERNAME is deliberately not among them: it is the household's own address, appears legitimately
// in half of what leaves, and as a registered secret the scanner would redact it everywhere.
const (
	SecretIMAPPassword = "mail.imap.password"
	SecretSMTPPassword = "mail.smtp.password"
)

// Outgoing is one message to send. Plain text only: an assistant writing HTML mail is a formatting
// problem nobody asked for, and every feature added here is a new way for generated content to leave.
type Outgoing struct {
	To      []string
	Subject string
	Body    string
}

// Send submits a message over the account's SMTP server.
//
// TLS is not optional on either path. The credential goes over this connection, so a server that
// offers no encryption is refused rather than downgraded — net/smtp's PlainAuth refuses as well, and
// two refusals for one mistake is the right number.
func Send(ctx context.Context, acct Account, password string, msg Outgoing) error {
	if len(msg.To) == 0 {
		return errors.New("mail: no recipient")
	}
	raw, err := compose(acct.From, msg, time.Now(), messageID(acct.From))
	if err != nil {
		return err
	}
	c, err := connectSMTP(ctx, acct, password)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.Mail(acct.From); err != nil {
		return fmt.Errorf("mail: from %s: %w", acct.From, err)
	}
	for _, to := range msg.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("mail: recipient %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		_ = w.Close()
		return fmt.Errorf("mail: write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: finish message: %w", err)
	}
	return c.Quit()
}

// connectSMTP opens an authenticated submission session. Shared by Send and Verify, which is the
// point: what a setup command checks has to be the same path a message later takes, or a verified
// account can still fail on the first real send.
func connectSMTP(ctx context.Context, acct Account, password string) (*smtp.Client, error) {
	host, port, err := net.SplitHostPort(acct.SMTPAddr)
	if err != nil {
		return nil, fmt.Errorf("mail: smtp address %q: %w", acct.SMTPAddr, err)
	}
	conn, err := dialSMTP(ctx, acct.SMTPAddr, host, port)
	if err != nil {
		return nil, err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mail: smtp %s: %w", acct.SMTPAddr, err)
	}
	if port != submissionTLSPort {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			_ = c.Close()
			return nil, fmt.Errorf("mail: %s offers no STARTTLS — refusing to send credentials in the clear", acct.SMTPAddr)
		}
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("mail: starttls with %s: %w", host, err)
		}
	}
	if err := c.Auth(smtp.PlainAuth("", acct.User, password, host)); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mail: authenticate as %s: %w", acct.User, err)
	}
	return c, nil
}

// submissionTLSPort is the implicit-TLS submission port. On it the connection is encrypted from the
// first byte; everywhere else (587, and anything a household configures) the session starts in the
// clear and STARTTLS is required before the credential moves.
const submissionTLSPort = "465"

// dialSMTP opens the transport and bounds the whole session on it, so STARTTLS, AUTH, RCPT and DATA
// are all covered — net/smtp takes no context, so the deadline is the only thing standing between a
// silent server and a turn that never ends.
func dialSMTP(ctx context.Context, addr, host, port string) (net.Conn, error) {
	// Bound the context before dialling, not after: the connection deadline set below never gets
	// applied if the handshake itself is where the server stalls.
	ctx, cancel := withSessionDeadline(ctx)
	defer cancel()

	var conn net.Conn
	var err error
	if port == submissionTLSPort {
		d := tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
		conn, err = d.DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("mail: dial %s: %w", addr, err)
	}
	if err := setSessionDeadline(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mail: %s: %w", addr, err)
	}
	return conn, nil
}

// compose renders the message as RFC 5322 bytes.
//
// The subject goes out as an RFC 2047 encoded word and the body as quoted-printable, which is what
// makes an umlaut survive the trip through servers that are older than the assistant sending it. now
// and id are parameters rather than read here, so the output is a pure function of its inputs and a
// test can compare it byte for byte.
func compose(from string, msg Outgoing, now time.Time, id string) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(msg.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", msg.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", id)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")

	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(msg.Body)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// messageID mints the identifier a mail is threaded and deduplicated by. Random rather than derived
// from the content: two identical reminders sent a week apart are two messages, and a server that
// deduplicated them would swallow the second.
func messageID(from string) string {
	domain := domainOf(from)
	if domain == "" {
		domain = "localhost"
	}
	return strings.ToLower(rand.Text()) + "@" + domain
}
