package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	netmail "net/mail"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// This file is the seam between the mailbox and the model. Three of the four tools are ungated and
// one is not, and the asymmetry is the whole design: reading is context, sending is an effect in the
// world that no one can take back. See ADR-17.

// maxLimit bounds how many messages one call may return. Not a performance limit — a cost one: a
// listing is folded into the model's context verbatim, and a mailbox has no upper bound.
const maxLimit = 50

// defaultLimit is what a call that names no limit gets: a screenful, not a mailbox.
const defaultLimit = 10

// maxBodyChars bounds the text of one message handed back. Past this a mail is a newsletter or a
// forwarded thread from 2019, and neither is worth the context it would cost.
const maxBodyChars = 16 << 10

// Config is what the mail tools need. The workspace assembles it: this package holds no vault handle
// and reads no configuration file.
type Config struct {
	// Account is the mailbox. Its password is NOT in it — see Password.
	Account Account

	// Password resolves a vault entry by name (SecretIMAPPassword, SecretSMTPPassword) at call time.
	// It is a function because secret.Store deliberately exposes presence and never value: the
	// workspace holds the vault handle and hands the value down, so the read stays at the composition
	// root instead of spreading into every package that needs a credential.
	Password func(name string) (string, bool)

	// Scanner blocks a stored secret on its way out and redacts one echoed back in. Nil = no scanning,
	// which is a workspace with no vault unlocked rather than a decision made here.
	Scanner *secret.Scanner

	// Log traces the effects — which folder was read, which recipients a message went to. Nil = silent.
	Log *slog.Logger
}

// Mailbox is the mail tool group.
type Mailbox struct {
	acct     Account
	password func(name string) (string, bool)
	scanner  *secret.Scanner
	log      *slog.Logger

	// The two effect paths, swappable so the tests drive the gate and the scanner without a server.
	// Production never sets them; New fills in Dial and Send.
	dial func(ctx context.Context, acct Account, password string) (*Client, error)
	send func(ctx context.Context, acct Account, password string, msg Outgoing) error
}

// New builds the mail tool group.
func New(cfg Config) *Mailbox {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Mailbox{
		acct:     cfg.Account,
		password: cfg.Password,
		scanner:  cfg.Scanner,
		log:      log,
		dial:     Dial,
		send:     Send,
	}
}

// Tools exposes the four mail tools.
//
// Searching is its own tool rather than a flag on listing, because the tool the model picks IS the
// intent — the same split as http_read against http_write. And sending is its own tool for a harder
// reason: it is the one that goes through the gate, so it must not be reachable by passing a field to
// something that reads.
func (m *Mailbox) Tools() ([]agentkit.Tool, error) {
	list, err := agentkit.NewTool("mail_list",
		"List the newest messages in a mail folder (newest first). Returns their UID, sender, subject, date and whether they are unread. Reading does not mark anything as read.",
		m.list,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("folder", agentkit.String("Folder to list (default INBOX)")),
			agentkit.Prop("limit", agentkit.Integer("How many messages (default 10, max 50)")),
		)),
	)
	if err != nil {
		return nil, err
	}
	search, err := agentkit.NewTool("mail_search",
		"Search the mailbox on the server. Matches text anywhere in a message, optionally narrowed by sender and by a date floor. Returns message headers, newest first. This is a substring search, not a semantic one.",
		m.search,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("text", agentkit.String("Text to find anywhere in the message")),
			agentkit.Prop("from", agentkit.String("Only messages whose sender contains this")),
			agentkit.Prop("since", agentkit.String("Only messages on or after this date (YYYY-MM-DD)")),
			agentkit.Prop("folder", agentkit.String("Folder to search (default INBOX)")),
			agentkit.Prop("limit", agentkit.Integer("How many messages (default 10, max 50)")),
		)),
	)
	if err != nil {
		return nil, err
	}
	read, err := agentkit.NewTool("mail_read",
		"Read one message by its UID, as plain text. A message with no plain-text part comes back with an empty body.",
		m.read,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("uid", agentkit.Integer("The message UID, as returned by mail_list or mail_search")),
			agentkit.Prop("folder", agentkit.String("Folder the message is in (default INBOX)")),
		).Require("uid")),
	)
	if err != nil {
		return nil, err
	}
	send, err := agentkit.NewTool("mail_send",
		"Send a plain-text message from the household's own address. This leaves the household and cannot be undone; each recipient requires approval.",
		m.sendTool,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("to", agentkit.Array(agentkit.String("A recipient address"), "Recipient addresses")),
			agentkit.Prop("subject", agentkit.String("Subject line")),
			agentkit.Prop("body", agentkit.String("Message text")),
		).Require("to", "subject", "body")),
	)
	if err != nil {
		return nil, err
	}
	return []agentkit.Tool{list, search, read, send}, nil
}

// connect opens a mailbox connection with the password out of the vault.
func (m *Mailbox) connect(ctx context.Context) (*Client, error) {
	password, ok := m.password(SecretIMAPPassword)
	if !ok {
		return nil, fmt.Errorf("no mail password stored — set it with: nocturn secret set %s", SecretIMAPPassword)
	}
	return m.dial(ctx, m.acct, password)
}

func (m *Mailbox) list(ctx context.Context, args string) (string, error) {
	var a struct {
		Folder string `json:"folder"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	c, err := m.connect(ctx)
	if err != nil {
		return "", err
	}
	defer c.Close()

	folder := folderOrDefault(a.Folder)
	headers, err := c.List(folder, limitOrDefault(a.Limit))
	if err != nil {
		return "", err
	}
	m.log.Debug("mail listed", "folder", folder, "messages", len(headers))
	return m.renderHeaders(headers)
}

func (m *Mailbox) search(ctx context.Context, args string) (string, error) {
	var a struct {
		Text   string `json:"text"`
		From   string `json:"from"`
		Since  string `json:"since"`
		Folder string `json:"folder"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	q := Query{Text: a.Text, From: a.From, Limit: limitOrDefault(a.Limit)}
	if a.Since != "" {
		since, err := time.Parse(time.DateOnly, a.Since)
		if err != nil {
			return "", fmt.Errorf("invalid since %q: expected YYYY-MM-DD", a.Since)
		}
		q.Since = since
	}
	c, err := m.connect(ctx)
	if err != nil {
		return "", err
	}
	defer c.Close()

	folder := folderOrDefault(a.Folder)
	headers, err := c.Search(folder, q)
	if err != nil {
		return "", err
	}
	m.log.Debug("mail searched", "folder", folder, "matches", len(headers))
	return m.renderHeaders(headers)
}

func (m *Mailbox) read(ctx context.Context, args string) (string, error) {
	var a struct {
		UID    uint32 `json:"uid"`
		Folder string `json:"folder"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.UID == 0 {
		return "", errors.New("missing required field: uid")
	}
	c, err := m.connect(ctx)
	if err != nil {
		return "", err
	}
	defer c.Close()

	folder := folderOrDefault(a.Folder)
	msg, err := c.Read(folder, a.UID)
	if err != nil {
		return "", err
	}
	if len(msg.Text) > maxBodyChars {
		msg.Text = msg.Text[:maxBodyChars] + "\n[…truncated]"
	}
	m.log.Debug("mail read", "folder", folder, "uid", a.UID)
	return m.marshalRedacted(map[string]any{
		"uid":     msg.UID,
		"from":    msg.From,
		"to":      msg.To,
		"subject": msg.Subject,
		"date":    msg.Date.Format(time.RFC3339),
		"body":    msg.Text,
	})
}

// sendTool is the gated path. The order is deliberate: every recipient clears the gate BEFORE the
// message is scanned, and both happen before a single byte reaches a server.
func (m *Mailbox) sendTool(ctx context.Context, args string) (string, error) {
	var a struct {
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Body    string   `json:"body"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if len(a.To) == 0 {
		return "", errors.New("missing required field: to")
	}
	// A recipient becomes three things: the sentence a human is asked about, the grant remembered
	// from that answer, and a To header line written verbatim. So it has to be an address and nothing
	// else — "chef@firma.de\r\nBcc: x@evil.de" ends in a domain and would pass a split on "@", while
	// framing the approval question around an address that is not the one being written down.
	// net/mail rejects the trailing header block, the newline in a display name and the missing
	// domain in one pass; what continues is its canonical form, never the raw argument.
	for i, to := range a.To {
		addr, err := netmail.ParseAddress(to)
		if err != nil || domainOf(addr.Address) == "" {
			return "", fmt.Errorf("invalid recipient %q", to)
		}
		a.To[i] = addr.Address
	}

	// One Check per recipient. A single ask covering three addresses is a question a person cannot
	// answer — and a grant remembered from it would carry an approval for each of them.
	for _, to := range a.To {
		if err := gate.Check(ctx, gate.Action{Kind: SendKind, Target: to}, AddressMatch, SendSuggestions(to)...); err != nil {
			return "", err
		}
	}

	// Egress scan AFTER the gate and before the send: the body is model output leaving the household,
	// which is the shortest exfiltration path in the tree. What comes back to the model says only that
	// the send was refused — unlike a rejected http_write there is nothing here for it to correct, and
	// naming the reason would tell it which text is a stored secret.
	if m.scanner != nil {
		if err := m.scanner.ScanEgress(append([]string{a.Subject, a.Body}, a.To...)...); err != nil {
			m.log.Warn("mail send blocked: the message carried a stored secret", "recipients", len(a.To), "err", err)
			return "", errors.New("sending was refused")
		}
	}

	password, ok := m.password(SecretSMTPPassword)
	if !ok {
		return "", fmt.Errorf("no mail password stored — set it with: nocturn secret set %s", SecretSMTPPassword)
	}
	if err := m.send(ctx, m.acct, password, Outgoing{To: a.To, Subject: a.Subject, Body: a.Body}); err != nil {
		return "", err
	}
	// Count and domains, never the addresses and never the subject. A log line is not the
	// vault-protected surface, and who the household writes to — with what in the subject — is the
	// content of the message, not a fact about the effect.
	domains := make([]string, 0, len(a.To))
	for _, to := range a.To {
		domains = append(domains, domainOf(to))
	}
	m.log.Info("mail sent", "recipients", len(a.To), "domains", domains)
	return m.marshalRedacted(map[string]any{"sent": true, "to": a.To})
}

// renderHeaders marshals a listing, redacting any vault value a message echoes back — mail is foreign
// text, so it is exactly the place a secret could return from.
func (m *Mailbox) renderHeaders(headers []Header) (string, error) {
	out := make([]map[string]any, 0, len(headers))
	for _, h := range headers {
		out = append(out, map[string]any{
			"uid":     h.UID,
			"from":    h.From,
			"subject": h.Subject,
			"date":    h.Date.Format(time.RFC3339),
			"unread":  !h.Seen,
		})
	}
	return m.marshalRedacted(map[string]any{"messages": out})
}

func (m *Mailbox) marshalRedacted(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if m.scanner != nil {
		b = m.scanner.RedactIngress(b)
	}
	return string(b), nil
}

func folderOrDefault(folder string) string {
	if folder == "" {
		return "INBOX"
	}
	return folder
}

func limitOrDefault(limit int) int {
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	}
	return limit
}
