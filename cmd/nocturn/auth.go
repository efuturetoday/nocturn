package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/internal/oauth"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// An oauth.Credential satisfies the credential seam (proven at compile time), so the
// injector can resolve the Gmail binding through a refreshing OAuth token.
var _ secret.Resolver = (*oauth.Credential)(nil)

// googleCredentialName is the secret name the Gmail binding resolves through —
// and the vault key its serialized oauth2.Token (refresh token included) lives
// under.
const googleCredentialName = "google"

// wireGoogleCredential runs the interactive OAuth ceremony (once — the token
// lives in the encrypted vault and is reused, refreshing thereafter) and
// registers a Bearer source for gmail.googleapis.com on inj. It is a no-op when
// GOOGLE_OAUTH_CLIENT_ID is unset, so the assistant runs fine without Gmail
// configured. Run it BEFORE bubbletea takes over the terminal (the consent URL
// is printed to stdout).
func wireGoogleCredential(ctx context.Context, inj *secret.Injector, vault *secret.Vault) error {
	clientID := os.Getenv("GOOGLE_OAUTH_CLIENT_ID")
	if clientID == "" {
		return nil
	}
	cfg := oauth.Google(clientID, os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"), googleScopes()...)

	tok, ok := vaultToken(vault, googleCredentialName)
	if !ok {
		var err error
		if tok, err = oauth.Authorize(ctx, cfg, nil); err != nil { // nil prompt = print the URL
			return fmt.Errorf("google authorization: %w", err)
		}
		if err := saveVaultToken(vault, googleCredentialName, tok); err != nil {
			return fmt.Errorf("persist token: %w", err)
		}
	}
	// The Gmail binding lives HERE, with the Gmail integration — not in the workspace
	// core. It says: an http effect to gmail.googleapis.com gets the "google" bearer,
	// host-injected at the boundary. Added only when Gmail is actually configured.
	inj.AddBinding("google", secret.Binding{
		Secret: googleCredentialName, Capability: "http", Host: "gmail.googleapis.com",
		Header: "Authorization", Prefix: "Bearer ",
	})
	inj.SetResolver(googleCredentialName, oauth.NewCredential(cfg, tok, persistToken(vault, googleCredentialName)))
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

// --- OAuth tokens in the vault -------------------------------------------
//
// A refresh token IS a secret, so the whole serialized oauth2.Token lives in
// the encrypted vault under the credential's secret name — the same name the
// injector's Resolver is registered under. Nothing token-shaped is written in
// the clear anymore; the vault re-seals on every change.

// vaultToken reads the oauth2.Token stored under name; ok is false if absent
// or invalid (no refresh token means we cannot refresh, so treat it as absent
// and re-auth).
func vaultToken(v *secret.Vault, name string) (*oauth2.Token, bool) {
	data, ok := v.Get(name)
	if !ok {
		return nil, false
	}
	return decodeToken(data)
}

// saveVaultToken serializes tok into the vault under name, which re-persists
// the encrypted vault file.
func saveVaultToken(v *secret.Vault, name string, tok *oauth2.Token) error {
	data, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return v.Set(name, data)
}

// persistToken is the onChange hook wired into an oauth.Credential: a refresh
// writes the fresh token (rotated refresh token included) back into the vault,
// best-effort — a failed persist must not fail the request the refresh served.
func persistToken(v *secret.Vault, name string) func(*oauth2.Token) {
	return func(tok *oauth2.Token) { _ = saveVaultToken(v, name, tok) }
}

func decodeToken(data []byte) (*oauth2.Token, bool) {
	var tok oauth2.Token
	if json.Unmarshal(data, &tok) != nil || tok.RefreshToken == "" {
		return nil, false
	}
	return &tok, true
}
