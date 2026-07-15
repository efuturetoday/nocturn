package oauth

import (
	"context"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// refreshTimeout bounds a token-refresh HTTP call.
const refreshTimeout = 15 * time.Second

// Source is the host-side, refreshing credential. It wraps an oauth2.TokenSource
// (which refreshes on expiry and is concurrency-safe) and yields the current
// access-token bytes — satisfying secret.Source structurally, without importing
// secret. When a refresh produces a new access token, onChange is invoked so the
// caller can persist it. The token never crosses into the guest; only the
// gateway boundary stamps it into an outbound request.
type Source struct {
	ts       oauth2.TokenSource
	onChange func(*oauth2.Token)

	mu   sync.Mutex
	last string // last access token handed out, to detect a refresh
}

// NewSource wraps cfg+initial into a refreshing Source. onChange (may be nil) is
// called with the fresh token whenever a refresh yields a new access token — wire
// it to persistence. Refresh I/O uses a background context with its own timeout,
// so a single request's cancellation can never kill the shared, long-lived token.
func NewSource(cfg *oauth2.Config, initial *oauth2.Token, onChange func(*oauth2.Token)) *Source {
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: refreshTimeout})
	return &Source{
		ts:       cfg.TokenSource(ctx, initial),
		onChange: onChange,
		last:     initial.AccessToken,
	}
}

// Value returns the current access token, transparently refreshing if it has
// expired. It is concurrency-safe (the underlying TokenSource single-flights the
// refresh). ctx is accepted for the secret.Source contract but does not bound the
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
