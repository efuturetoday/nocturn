package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/internal/oauth"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// An oauth.Source satisfies the credential seam (proven at compile time), so the
// injector can resolve the Gmail binding through a refreshing OAuth token.
var _ secret.Source = (*oauth.Source)(nil)

// googleCredentialName is the secret name the Gmail binding resolves through.
const googleCredentialName = "google"

// wireGoogleCredential runs the interactive OAuth ceremony (once — the token is
// persisted to disk and reused, refreshing thereafter) and registers a Bearer
// source for gmail.googleapis.com on inj. It is a no-op when GOOGLE_OAUTH_CLIENT_ID
// is unset, so the assistant runs fine without Gmail configured. Run it BEFORE
// bubbletea takes over the terminal (the consent URL is printed to stdout).
func wireGoogleCredential(ctx context.Context, inj *secret.Injector) error {
	clientID := os.Getenv("GOOGLE_OAUTH_CLIENT_ID")
	if clientID == "" {
		return nil
	}
	cfg := oauth.Google(clientID, os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"), googleScopes()...)

	tok, ok := loadToken()
	if !ok {
		var err error
		if tok, err = oauth.Authorize(ctx, cfg, nil); err != nil { // nil prompt = print the URL
			return fmt.Errorf("google authorization: %w", err)
		}
		if err := saveToken(tok); err != nil {
			return fmt.Errorf("persist token: %w", err)
		}
	}
	inj.SetSource(googleCredentialName, oauth.NewSource(cfg, tok, func(t *oauth2.Token) {
		_ = saveToken(t) // best-effort re-persist on refresh
	}))
	return nil
}

// googleScopes reads GOOGLE_OAUTH_SCOPES (space-separated); empty -> oauth.Google's
// default (Gmail read-only).
func googleScopes() []string {
	if s := strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_SCOPES")); s != "" {
		return strings.Fields(s)
	}
	return nil
}

// tokenPath is <user config dir>/nocturn/google.json.
func tokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nocturn", "google.json"), nil
}

// loadToken reads a previously persisted token; ok is false if none/invalid (no
// refresh token means we cannot refresh, so treat it as absent and re-auth).
func loadToken() (*oauth2.Token, bool) {
	p, err := tokenPath()
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var tok oauth2.Token
	if json.Unmarshal(data, &tok) != nil || tok.RefreshToken == "" {
		return nil, false
	}
	return &tok, true
}

// saveToken persists the token, dir 0700 / file 0600 (owner-only). It holds the
// long-lived refresh token; Keychain-encryption-at-rest is a later step.
func saveToken(tok *oauth2.Token) error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
