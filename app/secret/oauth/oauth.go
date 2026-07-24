// Package oauth runs the host side of OAuth2 (ADR-5): it obtains and refreshes
// access tokens so the gateway can inject a Bearer at the boundary. The guest
// NEVER sees a token — Credential yields the current access token host-side, and the
// credential injector stamps it in only for the bound destination host.
//
// It wraps golang.org/x/oauth2 for the flow (auth-code + PKCE + auto-refreshing,
// concurrency-safe TokenSource) and adds only what that library leaves to the
// application: the interactive authorization ceremony (a one-shot loopback
// listener) and a Credential adapter that persists refreshed tokens. This package
// does not import internal/secret; Credential satisfies secret.Resolver structurally.
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
)

// authTimeout bounds the interactive ceremony (the human has to click through).
const authTimeout = 3 * time.Minute

// Provider builds an OAuth2 config for ANY provider from its endpoints — so the
// host is provider-agnostic and a plugin can bring its own (auth_url, token_url,
// client_id, scopes) in its manifest. clientSecret may be "" for a public (PKCE)
// client, which is the norm for a shipped client_id. RedirectURL is set later by
// Authorize (the loopback address).
func Provider(authURL, tokenURL, clientID, clientSecret string, scopes ...string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL},
		Scopes:       scopes,
	}
}

// AuthCodeURL builds the authorization-request URL for redirectURI: PKCE S256
// challenge, offline access, and — when resource != "" — the RFC 8707 resource
// indicator naming the MCP server the token is FOR (audience binding; the MCP spec
// requires it on both the authorization and token requests). A bare "" resource
// omits it, for a non-MCP provider (a plugin's own OAuth) that does not use resource
// indicators. It sets cfg.RedirectURL so a later Exchange on the same cfg matches
// the redirect_uri the authorization server saw. This is the pure first half of the
// flow: the caller owns state, verifier and the redirect, so it works over a
// loopback (CLI) or a fixed app redirect (the companion app relays the code back).
func AuthCodeURL(cfg *oauth2.Config, state, verifier, resource, redirectURI string) string {
	cfg.RedirectURL = redirectURI
	opts := []oauth2.AuthCodeOption{
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.S256ChallengeOption(verifier),
	}
	if resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", resource))
	}
	return cfg.AuthCodeURL(state, opts...)
}

// Exchange is the pure second half: it swaps the authorization code for a token,
// sending the PKCE verifier and — matching the authorization request — the RFC 8707
// resource indicator. cfg must be the SAME config AuthCodeURL was called on (its
// RedirectURL has to equal the one the authorization server saw). No inbound socket
// is involved, so the caller may have obtained the code any way (loopback or app).
func Exchange(ctx context.Context, cfg *oauth2.Config, code, verifier, resource string) (*oauth2.Token, error) {
	opts := []oauth2.AuthCodeOption{oauth2.VerifierOption(verifier)}
	if resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", resource))
	}
	tok, err := cfg.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, fmt.Errorf("oauth: token exchange: %w", err)
	}
	return tok, nil
}

// GenerateState returns a random single-use state parameter (256 bits, URL-safe) that
// binds an authorization request to its callback. The caller stashes it and rejects a
// callback whose state does not match.
func GenerateState() (string, error) { return randomState() }

// Authorize runs the interactive authorization-code flow with PKCE and returns
// the token. It binds a one-shot loopback listener on 127.0.0.1 — the ONLY
// inbound socket in nocturn besides the unix socket, alive ONLY for this
// ceremony (bound to loopback, closed on return) — sets cfg.RedirectURL to it,
// calls prompt with the consent URL (default: print it; no browser exec), waits
// for the provider to redirect back with the code, and exchanges it. A random
// single-use state plus PKCE (S256) defend the callback. resource is the RFC 8707
// indicator (the MCP server URI) added to both requests, or "" for a provider
// that does not use it.
func Authorize(ctx context.Context, cfg *oauth2.Config, resource string, prompt func(url string)) (*oauth2.Token, error) {
	lb, err := NewLoopback()
	if err != nil {
		return nil, err
	}
	defer lb.Close()
	return lb.Authorize(ctx, cfg, resource, prompt)
}

// Loopback is the one-shot 127.0.0.1 redirect endpoint for the authorization-code
// callback — the ONLY inbound socket in nocturn besides the unix socket. Bind it
// with NewLoopback BEFORE Dynamic Client Registration (which needs the exact
// redirect URI), then run Authorize on it.
type Loopback struct {
	ln       net.Listener
	redirect string
}

// NewLoopback binds an ephemeral 127.0.0.1 port (never 0.0.0.0). Close it when done.
func NewLoopback() (*Loopback, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("oauth: loopback listen: %w", err)
	}
	return &Loopback{ln: ln, redirect: "http://" + ln.Addr().String() + "/callback"}, nil
}

// RedirectURL is the loopback callback URL to register as the client's redirect URI.
func (l *Loopback) RedirectURL() string { return l.redirect }

// Close releases the loopback socket.
func (l *Loopback) Close() error { return l.ln.Close() }

// WaitForCode serves the one-shot callback and returns the raw authorization code and
// the state the provider echoed back — it does NOT verify state (the caller holds the
// expected value and rejects a mismatch), so the same primitive backs both the CLI
// loopback and any other redirect catcher. It bounds the wait by authTimeout. A second
// duplicate callback is dropped rather than wedging its handler goroutine.
func (l *Loopback) WaitForCode(ctx context.Context) (code, state string, err error) {
	type result struct {
		code, state string
		err         error
	}
	results := make(chan result, 1)
	reply := func(res result) {
		select {
		case results <- res:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, e, http.StatusBadRequest)
			reply(result{err: fmt.Errorf("oauth: authorization denied: %s", e)})
			return
		}
		c := q.Get("code")
		if c == "" {
			http.Error(w, "no code", http.StatusBadRequest)
			reply(result{err: errors.New("oauth: no code in callback")})
			return
		}
		_, _ = w.Write([]byte("Authorization complete — you may close this tab."))
		reply(result{code: c, state: q.Get("state")})
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l.ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	ctx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return "", "", fmt.Errorf("oauth: authorization timed out: %w", ctx.Err())
	case res := <-results:
		return res.code, res.state, res.err
	}
}

// Authorize runs the whole interactive flow on this loopback: build the consent URL
// (PKCE + resource indicator), prompt the operator, wait for the redirect, verify
// state, and exchange the code. It is the convenience for the manual-provider CLI
// path; the MCP discover flow drives AuthCodeURL/WaitForCode/Exchange through the
// workspace so the app can share it.
func (l *Loopback) Authorize(ctx context.Context, cfg *oauth2.Config, resource string, prompt func(url string)) (*oauth2.Token, error) {
	if prompt == nil {
		prompt = func(u string) { fmt.Printf("Open this URL to authorize:\n%s\n", u) }
	}
	state, err := GenerateState()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()
	prompt(AuthCodeURL(cfg, state, verifier, resource, l.redirect))

	code, gotState, err := l.WaitForCode(ctx)
	if err != nil {
		return nil, err
	}
	if gotState != state {
		return nil, errors.New("oauth: state mismatch")
	}
	return Exchange(ctx, cfg, code, verifier, resource)
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth: random state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
