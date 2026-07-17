package main

import (
	"encoding/json"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/internal/oauth"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// An oauth.Credential satisfies the credential seam (proven at compile time), so the
// injector can resolve a plugin's OAuth binding through a refreshing token.
var _ secret.Resolver = (*oauth.Credential)(nil)

// --- OAuth tokens in the vault -------------------------------------------
//
// These helpers back plugin OAuth (wirePluginOAuth): every credential — including
// OAuth — comes from a plugin manifest; there is no built-in integration. A refresh
// token IS a secret, so the whole serialized oauth2.Token lives in the encrypted vault
// under the credential's secret name — the same name the injector's Resolver uses.
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
