package ntfy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Resolve is called with each decision token the listener receives from the
// response topic. hitl.Engine.Resolve satisfies it directly.
type Resolve func(token string) error

// Listener subscribes to the ntfy response topic and hands each decision token
// to Resolve — closing the out-of-band loop: a phone tap posts the token, ntfy
// streams it here, and the pending request is resolved. The daemon only makes
// this outbound streaming GET; it exposes no inbound port.
type Listener struct {
	client  *http.Client
	url     string // <baseURL>/<respTopic>/json
	auth    string
	resolve Resolve
	backoff time.Duration
}

// ListenerOption configures a Listener.
type ListenerOption func(*Listener)

// ListenerWithAuth sends "Authorization: Bearer <token>" for an access-
// controlled ntfy server.
func ListenerWithAuth(token string) ListenerOption {
	return func(l *Listener) { l.auth = "Bearer " + token }
}

// ListenerWithClient overrides the HTTP client (e.g. in tests).
func ListenerWithClient(c *http.Client) ListenerOption {
	return func(l *Listener) { l.client = c }
}

// NewListener returns a Listener streaming baseURL/<respTopic>/json.
func NewListener(baseURL, respTopic string, resolve Resolve, opts ...ListenerOption) *Listener {
	l := &Listener{
		client:  &http.Client{}, // no timeout: long-lived stream, stopped via ctx
		url:     baseURL + "/" + respTopic + "/json",
		resolve: resolve,
		backoff: 3 * time.Second,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Run subscribes and dispatches decision tokens until ctx is cancelled,
// reconnecting after transient stream errors.
func (l *Listener) Run(ctx context.Context) error {
	for {
		_ = l.stream(ctx)
		if ctx.Err() != nil {
			return ctx.Err() // cancelled: clean stop, cause observable
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(l.backoff):
		}
	}
}

func (l *Listener) stream(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return err
	}
	if l.auth != "" {
		req.Header.Set("Authorization", l.auth)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m struct {
			Event   string `json:"event"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(line, &m); err != nil {
			continue // ignore malformed lines
		}
		if m.Event == "message" && m.Message != "" {
			// Bad, expired, or already-used tokens are safely rejected inside
			// Resolve; nothing here needs to trust the token's authenticity.
			_ = l.resolve(m.Message)
		}
	}
	return sc.Err()
}
