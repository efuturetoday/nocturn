// Package oauth runs the host side of OAuth2 (ADR-5): it obtains and refreshes
// access tokens so the gateway can inject a Bearer at the boundary. The guest
// NEVER sees a token — Source yields the current access token host-side, and the
// credential injector stamps it in only for the bound destination host.
//
// It wraps golang.org/x/oauth2 for the flow (auth-code + PKCE + auto-refreshing,
// concurrency-safe TokenSource) and adds only what that library leaves to the
// application: the interactive authorization ceremony (a one-shot loopback
// listener) and a Source adapter that persists refreshed tokens. This package
// does not import internal/secret; Source satisfies secret.Source structurally.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// GmailReadonlyScope grants read-only access to the user's Gmail.
const GmailReadonlyScope = "https://www.googleapis.com/auth/gmail.readonly"

// authTimeout bounds the interactive ceremony (the human has to click through).
const authTimeout = 3 * time.Minute

// Google builds an OAuth2 config for Google's endpoints. clientSecret may be ""
// for a public (PKCE) desktop client. With no scopes it defaults to Gmail
// read-only. RedirectURL is set later by Authorize (the loopback address).
func Google(clientID, clientSecret string, scopes ...string) *oauth2.Config {
	if len(scopes) == 0 {
		scopes = []string{GmailReadonlyScope}
	}
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     endpoints.Google,
		Scopes:       scopes,
	}
}

// Authorize runs the interactive authorization-code flow with PKCE and returns
// the token. It binds a one-shot loopback listener on 127.0.0.1 — the ONLY
// inbound socket in nocturn besides the unix socket, alive ONLY for this
// ceremony (bound to loopback, closed on return) — sets cfg.RedirectURL to it,
// calls prompt with the consent URL (default: print it; no browser exec), waits
// for Google to redirect back with the code, and exchanges it. A random single-
// use state plus PKCE (S256) defend the callback. access_type=offline +
// prompt=consent are required for Google to return a refresh_token.
func Authorize(ctx context.Context, cfg *oauth2.Config, prompt func(url string)) (*oauth2.Token, error) {
	if prompt == nil {
		prompt = func(u string) { fmt.Println("Open this URL to authorize:\n" + u) }
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0") // loopback only, never 0.0.0.0
	if err != nil {
		return nil, fmt.Errorf("oauth: loopback listen: %w", err)
	}
	defer ln.Close()
	cfg.RedirectURL = "http://" + ln.Addr().String() + "/callback"

	state, err := randomState()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state { // reject a forged/mismatched callback
			http.Error(w, "bad state", http.StatusBadRequest)
			results <- result{err: errors.New("oauth: state mismatch")}
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, e, http.StatusBadRequest)
			results <- result{err: fmt.Errorf("oauth: authorization denied: %s", e)}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "no code", http.StatusBadRequest)
			results <- result{err: errors.New("oauth: no code in callback")}
			return
		}
		_, _ = w.Write([]byte("Authorization complete — you may close this tab."))
		results <- result{code: code}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	prompt(cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.S256ChallengeOption(verifier),
	))

	ctx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("oauth: authorization timed out: %w", ctx.Err())
	case res := <-results:
		if res.err != nil {
			return nil, res.err
		}
		tok, err := cfg.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("oauth: token exchange: %w", err)
		}
		return tok, nil
	}
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth: random state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
