package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/app/plugin"
	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/secret/oauth"
)

// oauthEntry pairs a plugin's OAuth declaration with the vault key its token lives under — the
// lowercased credential name, the SAME key installPlugins binds the credential's secret to. So an
// authorized token flows straight into that credential's injection.
type oauthEntry struct {
	decl       plugin.OAuthDecl
	secretName string
}

// discoverOAuth scans every workspace's plugins under root for OAuth providers. A plugin that fails
// to load is skipped (its own load path reports it); this is only about collecting providers.
func discoverOAuth(root string) []oauthEntry {
	var out []oauthEntry
	spaces, _ := os.ReadDir(root)
	for _, ws := range spaces {
		if !ws.IsDir() {
			continue
		}
		pluginsDir := filepath.Join(root, ws.Name(), "plugins")
		entries, _ := os.ReadDir(pluginsDir)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			loaded, err := plugin.Load(filepath.Join(pluginsDir, e.Name()))
			if err != nil {
				continue
			}
			for _, o := range loaded.Manifest.OAuth {
				out = append(out, oauthEntry{decl: o, secretName: strings.ToLower(o.Name)})
			}
		}
	}
	return out
}

// runAuth handles `nocturn auth <name>`: unlock the vault, find the OAuth provider named <name>, run
// the interactive authorization-code (+PKCE) flow, and store the resulting token in the vault so the
// daemon injects (and refreshes) it. The consent URL is printed to the terminal today; the same
// prompt seam will drive the companion app once it replaces the terminal.
func runAuth(ctx context.Context, name string) error {
	vault, err := openVault()
	if err != nil {
		return fmt.Errorf("unlock vault: %w", err)
	}
	if vault == nil {
		return errors.New("set NOCTURN_MASTER_PASSPHRASE to unlock the vault before connecting an account")
	}

	for _, e := range discoverOAuth(wsRoot) {
		if e.decl.Name != name {
			continue
		}
		cfg := oauth.Provider(e.decl.AuthURL, e.decl.TokenURL, e.decl.ClientID, e.decl.ClientSecret, e.decl.Scopes...)
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
	return fmt.Errorf("no OAuth provider named %q in any workspace's plugins", name)
}

// registerOAuth wires a refreshing token Source for each OAuth provider that already has a stored
// token, so the injector yields the live access token (refreshing on expiry) rather than the raw
// stored JSON. A refresh persists the new token back to the vault. Providers not yet authorized are
// left alone — their requests fail closed until `nocturn auth <name>` runs.
func registerOAuth(injector *secret.Injector, vault *secret.Vault, root string, log *slog.Logger) {
	for _, e := range discoverOAuth(root) {
		raw, ok := vault.Get(e.secretName)
		if !ok {
			continue // not authorized yet
		}
		var tok oauth2.Token
		if err := json.Unmarshal(raw, &tok); err != nil {
			log.Warn("oauth: stored token unreadable", "provider", e.decl.Name, "err", err)
			continue
		}
		cfg := oauth.Provider(e.decl.AuthURL, e.decl.TokenURL, e.decl.ClientID, e.decl.ClientSecret, e.decl.Scopes...)
		name := e.secretName
		injector.SetResolver(name, oauth.NewSource(cfg, &tok, func(t *oauth2.Token) {
			if err := storeToken(vault, name, t); err != nil {
				log.Warn("oauth: persist refreshed token", "provider", name, "err", err)
			}
		}))
		log.Info("oauth: token source registered", "provider", e.decl.Name)
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
