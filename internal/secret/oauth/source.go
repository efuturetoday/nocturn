package oauth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// refreshTimeout bounds a token-refresh HTTP call.
const refreshTimeout = 15 * time.Second

// resourceInjector appends the RFC 8707 resource indicator to a token-endpoint POST
// body, so a refresh request carries the SAME audience binding as the initial exchange.
// x/oauth2's TokenSource offers no hook for extra token-request parameters, so this
// RoundTripper adds it at the wire. It touches only form-encoded POSTs (the token
// endpoint) and never overwrites a resource the library might already set.
type resourceInjector struct {
	resource string
	base     http.RoundTripper
}

func (r resourceInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	base := r.base
	if base == nil {
		base = http.DefaultTransport
	}
	if r.resource == "" || req.Method != http.MethodPost || req.Body == nil ||
		!strings.HasPrefix(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return base.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		req.Body = io.NopCloser(bytes.NewReader(body)) // unparseable → send unchanged
		return base.RoundTrip(req)
	}
	if form.Get("resource") == "" {
		form.Set("resource", r.resource)
	}
	enc := form.Encode()
	req.Body = io.NopCloser(strings.NewReader(enc))
	req.ContentLength = int64(len(enc))
	return base.RoundTrip(req)
}

// Source is the host-side, refreshing credential. It wraps an oauth2.TokenSource
// (which refreshes on expiry and is concurrency-safe) and yields the current
// access-token bytes — satisfying secret.Resolver structurally, without importing
// secret. When a refresh produces a new access token, onChange is invoked so the
// caller can persist it. The token never crosses into the guest; only the
// gateway boundary stamps it into an outbound request.
type Source struct {
	ts       oauth2.TokenSource
	onChange func(*oauth2.Token)

	mu   sync.Mutex
	last string // last access token handed out, to detect a refresh
}

// NewSource wraps cfg+initial into a refreshing Source. resource is the RFC 8707
// indicator carried onto refresh requests (the MCP server URI, or "" for a provider
// that does not use it). onChange (may be nil) is called with the fresh token
// whenever a refresh yields a new access token — wire it to persistence. Refresh I/O
// uses a background context with its own timeout, so a single request's cancellation
// can never kill the shared, long-lived token.
func NewSource(cfg *oauth2.Config, initial *oauth2.Token, resource string, onChange func(*oauth2.Token)) *Source {
	client := &http.Client{Timeout: refreshTimeout, Transport: resourceInjector{resource: resource}}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	return &Source{
		ts:       cfg.TokenSource(ctx, initial),
		onChange: onChange,
		last:     initial.AccessToken,
	}
}

// Value returns the current access token, transparently refreshing if it has
// expired. It is concurrency-safe (the underlying TokenSource single-flights the
// refresh). ctx is accepted for the secret.Resolver contract but does not bound the
// refresh — the token outlives any single request (see NewSource).
func (s *Source) Value(context.Context) ([]byte, error) {
	tok, err := s.ts.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if tok.AccessToken != s.last {
		s.last = tok.AccessToken
		if s.onChange != nil {
			s.onChange(tok)
		}
	}
	s.mu.Unlock()
	return []byte(tok.AccessToken), nil
}
