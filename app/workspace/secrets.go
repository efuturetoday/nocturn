package workspace

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/app/mcp"
	"github.com/efuturetoday/nocturn/app/plugin"
	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/secret/oauth"
)

// This file is the per-workspace secret assembly. Each workspace opens its OWN encrypted vault —
// <dir>/vault.enc, keyed by the master's workspace-domain-separated sub-key — so a credential
// authorized in one workspace is encrypted under a different key, in a different file, on a
// different injector than any other's. The single master (one passphrase) is the root of every
// workspace key; nothing here holds the passphrase.

const vaultFile = "vault.enc"

// buildWorkspaceSecrets opens this workspace's vault and assembles its injector + scanner, seeding
// env secrets and per-workspace bindings and registering its plugins'/MCP servers' OAuth token
// sources. A nil master (vault locked, no passphrase) yields (nil, nil, nil): the workspace runs
// without host-owned credentials or leak scanning, exactly as before. The returned vault is held for
// its lifetime — an OAuth refresh persists the new token back through it.
func buildWorkspaceSecrets(master *secret.Master, dir, name string, log *slog.Logger) (*secret.Injector, *secret.Scanner, *secret.Vault, error) {
	if master == nil {
		return nil, nil, nil, nil
	}
	log = log.With("component", "secret")
	vault, err := secret.OpenVault(filepath.Join(dir, vaultFile), master.WorkspaceKey(name))
	if err != nil {
		return nil, nil, nil, err
	}
	injector := secret.NewInjector(vault.Store())
	scanner := secret.NewScanner(vault.Store())
	// Trace injection + leak-scan security events (names/rule-ids only, never a secret value) under
	// this workspace's component=secret logger.
	injector.SetLogger(log)
	scanner.SetLogger(log)
	seedEnvSecrets(vault, log)
	loadBindings(injector, filepath.Join(dir, "bindings.json"), log)
	registerOAuth(injector, vault, dir, log)
	log.Info("secret: workspace vault unlocked", "ws", name)
	return injector, scanner, vault, nil
}

// OAuthProvider is one authorizable OAuth provider a workspace exposes — source-agnostic (plugin or
// MCP), carrying the provider name (`nocturn auth <name>`), its owner-namespaced vault key, and the
// flow's endpoints. It is the hand-off between the source packages' discovery and both the daemon's
// per-workspace registration (registerOAuth) and the CLI auth flow (main's runAuth).
type OAuthProvider struct {
	Name         string
	SecretName   string
	AuthURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// DiscoverOAuth aggregates the OAuth providers a single workspace declares — its plugins' and its
// MCP servers' — each already carrying its owner-namespaced vault key. Every source owns its own
// config parsing and key derivation (plugin.DiscoverOAuth, mcp.DiscoverOAuth); this only adapts them
// to the shared descriptor, so no plugin/mcp config shape leaks into the OAuth wiring.
func DiscoverOAuth(wsDir string) []OAuthProvider {
	var out []OAuthProvider
	for _, p := range plugin.DiscoverOAuth(wsDir) {
		out = append(out, OAuthProvider(p))
	}
	for _, p := range mcp.DiscoverOAuth(wsDir) {
		out = append(out, OAuthProvider(p))
	}
	return out
}

// registerOAuth wires a refreshing token Source for each of the workspace's OAuth providers that
// already has a stored token, so the injector yields a live access token (refreshing on expiry). A
// refresh persists the new token back to this workspace's vault. Providers not yet authorized are
// left alone — their requests fail closed until `nocturn auth <name>` runs.
func registerOAuth(injector *secret.Injector, vault *secret.Vault, wsDir string, log *slog.Logger) {
	log = log.With("component", "oauth")
	for _, p := range DiscoverOAuth(wsDir) {
		raw, ok := vault.Get(p.SecretName)
		if !ok {
			continue // not authorized yet
		}
		var tok oauth2.Token
		if err := json.Unmarshal(raw, &tok); err != nil {
			log.Warn("oauth: stored token unreadable", "provider", p.Name, "err", err)
			continue
		}
		cfg := oauth.Provider(p.AuthURL, p.TokenURL, p.ClientID, p.ClientSecret, p.Scopes...)
		name := p.SecretName
		injector.SetResolver(name, oauth.NewSource(cfg, &tok, func(t *oauth2.Token) {
			if err := StoreToken(vault, name, t); err != nil {
				log.Warn("oauth: persist refreshed token", "provider", name, "err", err)
			}
		}))
		log.Info("oauth: token source registered", "provider", p.Name)
	}
}

// StoreToken persists an OAuth token as JSON in the vault under name (encrypted at rest). Exported so
// the CLI auth flow (main) stores into the target workspace's vault through the same path.
func StoreToken(vault *secret.Vault, name string, tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return vault.Set(name, b)
}

// seedEnvSecrets stores each NOCTURN_SECRET_<NAME>=value into the vault under <name> (lowercased) —
// the input channel for credential values until an interactive add-secret UX exists. The same env is
// seeded into every workspace's vault (a shared input), but each copy lives in an isolated vault.
func seedEnvSecrets(vault *secret.Vault, log *slog.Logger) {
	const prefix = "NOCTURN_SECRET_"
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, prefix) || v == "" {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(k, prefix))
		if err := vault.Set(name, []byte(v)); err != nil {
			log.Warn("secret: seed", "name", name, "err", err)
		}
	}
}

// loadBindings reads a workspace's bindings.json (a list of host-owned credential bindings) and
// registers each at the workspace level (owner ""), so the model's own network calls inject them.
// Absent file = none.
func loadBindings(inj *secret.Injector, path string, log *slog.Logger) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // no bindings configured
	}
	var raw []struct {
		Secret string `json:"secret"`
		Host   string `json:"host"`
		Header string `json:"header"`
		Prefix string `json:"prefix"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Warn("secret: bindings.json", "err", err)
		return
	}
	for _, b := range raw {
		inj.AddBinding("", secret.Binding{Secret: b.Secret, Host: b.Host, Header: b.Header, Prefix: b.Prefix})
	}
	log.Info("secret: bindings loaded", "count", len(raw))
}
