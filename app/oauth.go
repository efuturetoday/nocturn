package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/app/mcp"
	"github.com/efuturetoday/nocturn/app/plugin"
	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/secret/oauth"
)

// oauthEntry is one authorizable OAuth provider, source-agnostic: the provider name (what `nocturn
// auth <name>` matches), the vault key its token lives under (which the injecting binding also names,
// so an authorized token flows straight into injection), and the flow's endpoints. Every source
// package (plugin, mcp) discovers its own providers and hands them here — this file holds no plugin/
// mcp config knowledge, only the wiring.
type oauthEntry struct {
	name         string
	secretName   string
	authURL      string
	tokenURL     string
	clientID     string
	clientSecret string
	scopes       []string
}

// discoverAllOAuth aggregates every OAuth provider the daemon can authorize across its sources. Each
// source owns its own discovery (config parsing + vault-key derivation) and returns a flat descriptor;
// this only adapts them to the shared oauthEntry. A new source = one DiscoverOAuth + one loop here.
func discoverAllOAuth(root string) []oauthEntry {
	var entries []oauthEntry
	for _, p := range plugin.DiscoverOAuth(root) {
		entries = append(entries, oauthEntry{
			name: p.Name, secretName: p.SecretName,
			authURL: p.AuthURL, tokenURL: p.TokenURL,
			clientID: p.ClientID, clientSecret: p.ClientSecret, scopes: p.Scopes,
		})
	}
	for _, p := range mcp.DiscoverOAuth(root) {
		entries = append(entries, oauthEntry{
			name: p.Name, secretName: p.SecretName,
			authURL: p.AuthURL, tokenURL: p.TokenURL,
			clientID: p.ClientID, clientSecret: p.ClientSecret, scopes: p.Scopes,
		})
	}
	return entries
}

// runAuth handles `nocturn auth <name>`: unlock the vault, find the OAuth provider named <name>, run
// the interactive authorization-code (+PKCE) flow, and store the resulting token in the vault so the
// daemon injects (and refreshes) it. The consent URL is printed to the terminal today; the same
// prompt seam will drive the companion app once it replaces the terminal. A plugin and an MCP server
// may share a name — both are authorized, to their distinct vault keys, so no credential crosses.
func runAuth(ctx context.Context, name string) error {
	vault, err := openVault()
	if err != nil {
		return fmt.Errorf("unlock vault: %w", err)
	}
	if vault == nil {
		return errors.New("set NOCTURN_MASTER_PASSPHRASE to unlock the vault before connecting an account")
	}

	// Match a provider by its human name OR its exact owner-namespaced secretName (the latter lets a
	// user disambiguate when a plugin and an MCP server share a name). A bare name that matches more
	// than one is ambiguous and refused — authorizing "all of them" would run several browser flows
	// and store several tokens off one command.
	all := discoverAllOAuth(wsRoot)
	var matches []oauthEntry
	for _, e := range all {
		if e.name == name || e.secretName == name {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("no OAuth provider named %q in any workspace's plugins or MCP servers", name)
	case 1:
		// authorize below
	default:
		ids := make([]string, len(matches))
		for i, e := range matches {
			ids[i] = e.secretName
		}
		return fmt.Errorf("provider %q is ambiguous — qualify one of: %s", name, strings.Join(ids, ", "))
	}

	e := matches[0]
	cfg := oauth.Provider(e.authURL, e.tokenURL, e.clientID, e.clientSecret, e.scopes...)
	tok, err := oauth.Authorize(ctx, cfg, func(u string) {
		fmt.Printf("\nOpen this URL to authorize %q, then return here:\n\n  %s\n\n", name, u)
	})
	if err != nil {
		return err
	}
	if err := storeToken(vault, e.secretName, tok); err != nil {
		return fmt.Errorf("store token: %w", err)
	}
	fmt.Printf("connected %q — the daemon will inject and refresh its token.\n", name)
	return nil
}

// registerOAuth wires a refreshing token Source for each OAuth provider that already has a stored
// token, so the injector yields the live access token (refreshing on expiry) rather than the raw
// stored JSON. A refresh persists the new token back to the vault. Providers not yet authorized are
// left alone — their requests fail closed until `nocturn auth <name>` runs.
func registerOAuth(injector *secret.Injector, vault *secret.Vault, root string, log *slog.Logger) {
	log = log.With("component", "oauth")
	for _, e := range discoverAllOAuth(root) {
		raw, ok := vault.Get(e.secretName)
		if !ok {
			continue // not authorized yet
		}
		var tok oauth2.Token
		if err := json.Unmarshal(raw, &tok); err != nil {
			log.Warn("oauth: stored token unreadable", "provider", e.name, "err", err)
			continue
		}
		cfg := oauth.Provider(e.authURL, e.tokenURL, e.clientID, e.clientSecret, e.scopes...)
		name := e.secretName
		injector.SetResolver(name, oauth.NewSource(cfg, &tok, func(t *oauth2.Token) {
			if err := storeToken(vault, name, t); err != nil {
				log.Warn("oauth: persist refreshed token", "provider", name, "err", err)
			}
		}))
		log.Info("oauth: token source registered", "provider", e.name)
	}
}

// storeToken persists an OAuth token as JSON in the vault under name (encrypted at rest).
func storeToken(vault *secret.Vault, name string, tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return vault.Set(name, b)
}
