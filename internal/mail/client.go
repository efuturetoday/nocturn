package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
)

// This file is the mailbox READING side, and it is a deliberately narrow facade over go-imap: list
// the recent headers of a folder, read one message as text. Everything the library can do beyond that
// — IDLE, server-side search, moving and flagging — stays unexposed until a tool needs it, because
// every method here becomes something the model can reach for.
//
// The library stays behind this type. A tool never holds an *imapclient.Client, so swapping the
// implementation (or the whole protocol, for a JMAP account) is local to this file. See ADR-17 for
// why the library was taken rather than written.

// Account is one mailbox: where it lives and who it belongs to. The password is NOT here — it is
// resolved from the vault at dial time and never stored beside the configuration, which is what lets
// the configuration be a plain readable file.
type Account struct {
	IMAPAddr string // host:port, TLS
	SMTPAddr string // host:port, submission
	User     string // the login, usually the address itself
	From     string // the envelope sender outgoing mail carries
}

// Header is what a listing shows: enough to decide whether a message is worth reading, and the UID to
// read it by. Subject and From are already decoded out of their RFC 2047 form.
type Header struct {
	UID     uint32
	From    string
	Subject string
	Date    time.Time
	Seen    bool
}

// Message is one mail, flattened to what a language model can use: its header plus the text/plain
// body. HTML-only mail yields an empty Text rather than a wall of markup, and attachments are not
// carried at all — a tool that needs them can grow its own accessor.
type Message struct {
	Header
	To   []string
	Text string
}

// Client is a connected mailbox. It is NOT safe for concurrent use: one connection carries one
// selected folder, so two callers would race over which folder is open. Callers dial per operation.
type Client struct {
	c *imapclient.Client
}

// Dial opens a TLS connection to the account's IMAP server and logs in.
//
// ctx bounds the DIAL only. go-imap's commands do not take a context, so a server that accepts the
// connection and then stalls is bounded by the deadline set on the connection, not by cancellation —
// the honest version of "ctx-aware" here is to say which half it covers.
func Dial(ctx context.Context, acct Account, password string) (*Client, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{MinVersion: tls.VersionTLS12}}
	conn, err := d.DialContext(ctx, "tcp", acct.IMAPAddr)
	if err != nil {
		return nil, fmt.Errorf("mail: dial %s: %w", acct.IMAPAddr, err)
	}
	return NewClient(conn, acct.User, password)
}

// NewClient logs in over an already-established connection and takes ownership of it — the transport seam,
// so the tests drive a scripted server over a pipe instead of a socket with a certificate. Dial is
// the production path; nothing outside a test builds the connection itself.
func NewClient(conn net.Conn, user, password string) (*Client, error) {
	// Decode RFC 2047 encoded-words with go-message's charset table. Without it a subject in
	// ISO-8859-1 or ISO-2022-JP arrives as its raw encoded form, which is the swamp this library was
	// taken for in the first place.
	c := imapclient.New(conn, &imapclient.Options{
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	})
	if err := c.Login(user, password).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("mail: login as %s: %w", user, err)
	}
	return &Client{c: c}, nil
}

// Close logs out and closes the connection.
func (c *Client) Close() error {
	if err := c.c.Logout().Wait(); err != nil {
		c.c.Close() // log out failed; the socket still has to go
		return fmt.Errorf("mail: logout: %w", err)
	}
	return c.c.Close()
}

// List returns the newest messages of a folder, most recent first, at most limit of them.
//
// Newest-first is the useful order and it is also the safe one: a mailbox has no upper bound, so a
// listing that started at the beginning would spend the model's whole context on mail from years ago.
func (c *Client) List(folder string, limit int) ([]Header, error) {
	if limit <= 0 {
		return nil, nil
	}
	sel, err := c.c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("mail: select %q: %w", folder, err)
	}
	if sel.NumMessages == 0 {
		return nil, nil
	}
	from := uint32(1)
	if n := uint32(limit); sel.NumMessages > n {
		from = sel.NumMessages - n + 1
	}
	var set imap.SeqSet
	set.AddRange(from, sel.NumMessages)

	msgs, err := c.c.Fetch(set, &imap.FetchOptions{UID: true, Envelope: true, Flags: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("mail: fetch headers from %q: %w", folder, err)
	}
	out := make([]Header, 0, len(msgs))
	for _, m := range slices.Backward(msgs) { // Collect yields ascending sequence numbers; newest first
		out = append(out, headerOf(m))
	}
	return out, nil
}

// Query is what a search asks for. Every field is optional and they narrow together (an "and"): text
// in the message, a sender, a date floor. An empty Query is every message in the folder, newest
// first, which is exactly what List does.
type Query struct {
	Text  string    // appears anywhere in headers or body
	From  string    // substring of the From header
	Since time.Time // only the date is used; the server ignores time and zone
	Limit int       // at most this many, newest first
}

// Search runs the query on the SERVER and returns the matching headers, newest first.
//
// Server-side is the point. A mailbox is the one corpus in the household that is unbounded and not
// ours, so the alternatives are both bad: pulling it down to grep locally moves a decade of mail over
// the wire, and embedding it into the knowledge index would let anyone who knows the address write
// into the assistant's retrieval corpus. IMAP has had SEARCH since 1996; it costs one command and
// copies nothing.
//
// What it does NOT do is semantics — SEARCH matches substrings, so "roof" does not find "Dachdecker".
// That limit is real and is the reason a mail-specific index may still be worth building later, with
// its own corpus and its own provenance.
func (c *Client) Search(folder string, q Query) ([]Header, error) {
	limit := q.Limit
	if limit <= 0 {
		return nil, nil
	}
	if _, err := c.c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, fmt.Errorf("mail: select %q: %w", folder, err)
	}
	criteria := &imap.SearchCriteria{Since: q.Since}
	if q.Text != "" {
		criteria.Text = []string{q.Text}
	}
	if q.From != "" {
		criteria.Header = []imap.SearchCriteriaHeaderField{{Key: "From", Value: q.From}}
	}
	data, err := c.c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("mail: search %q: %w", folder, err)
	}
	uids := data.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}
	if len(uids) > limit { // the server answers ascending; the newest are the tail
		uids = uids[len(uids)-limit:]
	}
	msgs, err := c.c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID: true, Envelope: true, Flags: true,
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("mail: fetch matches from %q: %w", folder, err)
	}
	out := make([]Header, 0, len(msgs))
	for _, m := range slices.Backward(msgs) {
		out = append(out, headerOf(m))
	}
	return out, nil
}

// Read returns one message of a folder by UID, with its text/plain body.
//
// The fetch PEEKS: reading a mailbox through the assistant must not mark the household's mail as
// seen. A person's unread list is theirs, and a background agent skimming the inbox at six in the
// morning would otherwise quietly empty it.
func (c *Client) Read(folder string, uid uint32) (Message, error) {
	if _, err := c.c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return Message{}, fmt.Errorf("mail: select %q: %w", folder, err)
	}
	section := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
		UID:         true,
		Envelope:    true,
		Flags:       true,
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return Message{}, fmt.Errorf("mail: fetch uid %d from %q: %w", uid, folder, err)
	}
	if len(msgs) == 0 {
		return Message{}, fmt.Errorf("mail: uid %d in %q: %w", uid, folder, ErrNotFound)
	}
	buf := msgs[0]
	m := Message{Header: headerOf(buf)}
	if buf.Envelope != nil {
		m.To = addresses(buf.Envelope.To)
	}
	text, err := plainText(buf.FindBodySection(section))
	if err != nil {
		return Message{}, fmt.Errorf("mail: parse uid %d: %w", uid, err)
	}
	m.Text = text
	return m, nil
}

// ErrNotFound reports a UID the server does not have — a message moved or deleted between a listing
// and a read, which is ordinary rather than exceptional, so callers branch on it.
var ErrNotFound = errors.New("message not found")

func headerOf(buf *imapclient.FetchMessageBuffer) Header {
	h := Header{UID: uint32(buf.UID)}
	if buf.Envelope != nil {
		h.Subject = buf.Envelope.Subject
		h.Date = buf.Envelope.Date
		if from := addresses(buf.Envelope.From); len(from) > 0 {
			h.From = from[0]
		}
	}
	for _, f := range buf.Flags {
		if f == imap.FlagSeen {
			h.Seen = true
		}
	}
	return h
}

func addresses(addrs []imap.Address) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.Addr())
	}
	return out
}

// plainText pulls the text/plain body out of a raw message, decoding transfer encoding and charset on
// the way. A message with no plain part yields "" and no error: HTML-only mail is common and is not a
// failure, and handing the model a page of markup would be worse than handing it nothing.
func plainText(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	defer r.Close()
	var b strings.Builder
	for {
		p, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		h, ok := p.Header.(*gomail.InlineHeader)
		if !ok {
			continue // an attachment, not something to read
		}
		t, _, err := h.ContentType()
		if err != nil || t != "text/plain" {
			continue
		}
		body, err := io.ReadAll(p.Body)
		if err != nil {
			return "", err
		}
		b.Write(body)
	}
	return b.String(), nil
}
