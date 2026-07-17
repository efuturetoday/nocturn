// Package ntfy is the real out-of-band push transport: a thin implementation of
// hitl.Notifier over ntfy (https://ntfy.sh or a self-hosted server). It
// publishes an approval request with Approve/Deny action buttons; tapping a
// button posts the corresponding signed token to a response topic, which the
// daemon (subscribed to that topic) hands to hitl.Engine.Resolve.
//
// The daemon only makes outbound connections — publish here, subscribe for
// responses elsewhere — so no inbound port is exposed. Channel privacy relies
// on a hard-to-guess topic plus, for real use, a self-hosted authenticated
// ntfy; the HMAC single-use token is the actual integrity control (a reader of
// the topic still cannot forge an approval).
package ntfy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/efuturetoday/nocturn/internal/hitl"
)

// Publisher publishes approval requests to an ntfy topic.
type Publisher struct {
	client   *http.Client
	baseURL  string // ntfy server root, e.g. "https://ntfy.sh"
	reqTopic string // topic approval requests are published to
	respURL  string // URL the Approve/Deny buttons POST their token to
	auth     string // optional Authorization header value
}

// Option configures a Publisher.
type Option func(*Publisher)

// WithAuth sends "Authorization: Bearer <token>" — needed for a self-hosted,
// access-controlled ntfy.
func WithAuth(token string) Option {
	return func(p *Publisher) { p.auth = "Bearer " + token }
}

// WithClient overrides the HTTP client (e.g. in tests).
func WithClient(c *http.Client) Option {
	return func(p *Publisher) { p.client = c }
}

// New returns a Publisher that posts requests to baseURL for reqTopic, with
// action buttons that POST the decision token to respURL.
func New(baseURL, reqTopic, respURL string, opts ...Option) *Publisher {
	p := &Publisher{
		client:   &http.Client{Timeout: 10 * time.Second},
		baseURL:  baseURL,
		reqTopic: reqTopic,
		respURL:  respURL,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// compile-time proof that *Publisher is a hitl.Notifier.
var _ hitl.Notifier = (*Publisher)(nil)

type action struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	URL    string `json:"url"`
	Method string `json:"method,omitempty"`
	Body   string `json:"body,omitempty"`
	Clear  bool   `json:"clear,omitempty"`
}

type message struct {
	Topic   string   `json:"topic"`
	Title   string   `json:"title,omitempty"`
	Message string   `json:"message"`
	Actions []action `json:"actions,omitempty"`
}

// Notify publishes the approval request with one action button per option, each
// carrying its token. (ntfy renders up to 3 action buttons.)
func (p *Publisher) Notify(intent string, options []hitl.Option) error {
	actions := make([]action, 0, len(options))
	for _, o := range options {
		actions = append(actions, action{
			Action: "http", Label: o.Label, URL: p.respURL, Method: http.MethodPost, Body: o.Token, Clear: true,
		})
	}
	return p.post(context.Background(), message{
		Topic:   p.reqTopic,
		Title:   "Nocturn: approval needed",
		Message: intent,
		Actions: actions,
	})
}

// Push publishes a plain notification to the user's channel — NO action buttons,
// no token. Unlike Notify (an approval REQUEST that waits for a decision), this is
// fire-and-forget: the assistant proactively telling the user something. It
// satisfies notifycap.Pusher.
func (p *Publisher) Push(ctx context.Context, title, msg string) error {
	if title == "" {
		title = "Nocturn"
	}
	return p.post(ctx, message{Topic: p.reqTopic, Title: title, Message: msg})
}

// post marshals m and POSTs it to the ntfy server, shared by Notify and Push.
func (p *Publisher) post(ctx context.Context, m message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.auth != "" {
		req.Header.Set("Authorization", p.auth)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ntfy: publish failed: %s", resp.Status)
	}
	return nil
}
