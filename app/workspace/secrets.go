package workspace

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/app/discovery"
	"github.com/efuturetoday/nocturn/app/mcp"
	"github.com/efuturetoday/nocturn/app/mcp/authflow"
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
	log = log.With("component", "secret")
	if master == nil {
		log.Info("vault locked (no master passphrase) — running without host-owned credentials")
		return nil, nil, nil, nil
	}
	vault, err := secret.OpenVault(filepath.Join(dir, vaultFile), master.WorkspaceKey(name))
	if err != nil {
		return nil, nil, nil, err
	}
	seedEnvSecrets(vault, log)
	// The injector + scanner resolve over a UNION resolution store: the workspace vault's own
	// secrets PLUS every plugin/mcp shard's (each secrets.enc decrypted with its folder-path key).
	// This store lives only in memory and is NEVER persisted, so a write to the workspace vault (an
	// OAuth refresh) can never leak a shard secret into vault.enc — compartmentalization holds on
	// disk. Shards fail closed: a bad one is absent, not a fallback to the workspace vault.
	res := secret.NewStore()
	vault.Store().CopyInto(res)
	secret.LoadShardsInto(res, master, dir, name, discovery.ValidName, log)
	injector := secret.NewInjector(res)
	scanner := secret.NewScanner(res)
	// Trace injection + leak-scan security events (names/rule-ids only, never a secret value) under
	// this workspace's component=secret logger.
	injector.SetLogger(log)
	scanner.SetLogger(log)
	loadBindings(injector, filepath.Join(dir, "bindings.json"), log)
	// OAuth tokens live in each plugin/mcp folder's shard (path-encrypted), not the workspace vault —
	// registerOAuth reads and refreshes them through the shard router, keyed by the credential's name.
	registerOAuth(injector, NewShardTokens(master, dir, name, log), dir, log)
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

// TokenStore reads and persists a credential's token by its logical, owner-namespaced SecretName —
// the OAuth wiring addresses credentials by identity, never by storage location. ShardTokens is the
// on-disk implementation (per-folder shard); an external vault (1Password) would satisfy the same
// seam without OAuth learning anything about where the token lives.
type TokenStore interface {
	Get(secretName string) ([]byte, bool)
	Set(secretName string, value []byte) error
}

// ShardTokens routes a credential's SecretName to the per-owner secret shard it belongs to. It is the
// composition root's job to know the plugin:/mcp: owner conventions and the sharding; secret stays
// mechanism-only (secret.OpenShard by relPath) and the oauth package stays flow-only. It holds no
// open handles — a token read/write opens its shard transiently (auth + refresh are rare).
type ShardTokens struct {
	master        *secret.Master
	wsDir, wsName string
	log           *slog.Logger // may be nil (a one-shot CLI write does not read)
}

// NewShardTokens builds the token store for one workspace. Exported so the CLI auth flow uses the
// same routing the daemon does. log may be nil.
func NewShardTokens(master *secret.Master, wsDir, wsName string, log *slog.Logger) ShardTokens {
	return ShardTokens{master: master, wsDir: wsDir, wsName: wsName, log: log}
}

// relPath maps an owner-namespaced SecretName to its shard folder. ok is false for a name that is not
// a shard-owned credential (a bare workspace secret), so it is never mis-routed.
func (s ShardTokens) relPath(secretName string) (string, bool) {
	if rest, ok := strings.CutPrefix(secretName, "plugin:"); ok {
		if folder, _, _ := strings.Cut(rest, "/"); folder != "" {
			return "plugins/" + folder, true
		}
	}
	if rest, ok := strings.CutPrefix(secretName, "mcp:"); ok {
		if folder, _, _ := strings.Cut(rest, "@"); folder != "" {
			return "mcp/" + folder, true
		}
	}
	return "", false
}

// Get reads a token from its shard; ok is false when the name is not shard-owned or its shard/entry
// is absent (not yet authorized) — fail-closed, never a fallback to another store.
func (s ShardTokens) Get(secretName string) ([]byte, bool) {
	rp, ok := s.relPath(secretName)
	if !ok {
		return nil, false
	}
	if _, err := os.Stat(secret.ShardPath(s.wsDir, rp)); err != nil {
		return nil, false // no shard file → not authorized (the normal case)
	}
	sv, err := secret.OpenShard(s.master, s.wsDir, s.wsName, rp)
	if err != nil {
		// The shard EXISTS but won't open — corrupt, tampered, or a wrong key. Distinct from "not
		// authorized", so surface it; still fail-closed (the credential stays absent, no fallback).
		if s.log != nil {
			s.log.Warn("secret: oauth shard unreadable", "shard", rp, "err", err)
		}
		return nil, false
	}
	return sv.Get(secretName)
}

// Set writes a token into its shard (creating the shard file if needed).
func (s ShardTokens) Set(secretName string, value []byte) error {
	rp, ok := s.relPath(secretName)
	if !ok {
		return fmt.Errorf("secret %q is not a shard-owned credential", secretName)
	}
	sv, err := secret.OpenShard(s.master, s.wsDir, s.wsName, rp)
	if err != nil {
		return err
	}
	return sv.Set(secretName, value)
}

// registerOAuth wires a refreshing token Source for each of the workspace's OAuth providers that
// already has a stored token, so the injector yields a live access token (refreshing on expiry). The
// token lives in the owning plugin/mcp folder's shard; a refresh persists it back there. Providers
// not yet authorized are left alone — their requests fail closed until `nocturn auth <name>` runs.
func registerOAuth(injector *secret.Injector, tokens TokenStore, wsDir string, log *slog.Logger) {
	log = log.With("component", "oauth")
	// Plugin OAuth: endpoints from the manifest, no resource indicator (not an MCP resource).
	for _, p := range plugin.DiscoverOAuth(wsDir) {
		wireOAuth(injector, tokens, p.SecretName, p.Name, OAuthRecord{
			AuthURL: p.AuthURL, TokenURL: p.TokenURL, ClientID: p.ClientID, ClientSecret: p.ClientSecret, Scopes: p.Scopes,
		}, log)
	}
	// MCP OAuth: a manual block's endpoints from config, or a persisted record from discovery. Both
	// carry the RFC 8707 resource (the server's canonical URI) so refresh stays audience-bound.
	for _, srv := range mcp.Discover(filepath.Join(wsDir, "mcp"), nil).All() {
		sn, ok := mcpSecretName(srv)
		if !ok {
			continue
		}
		switch srv.OAuthMode() {
		case mcp.AuthManual:
			resource, _ := authflow.CanonicalResource(srv.URL)
			wireOAuth(injector, tokens, sn, srv.Name, OAuthRecord{
				AuthURL: srv.OAuth.AuthURL, TokenURL: srv.OAuth.TokenURL, ClientID: srv.OAuth.ClientID,
				ClientSecret: srv.OAuth.ClientSecret, Resource: resource, Scopes: srv.OAuth.Scopes,
			}, log)
		case mcp.AuthDiscover:
			if rec, ok := LoadOAuthRecord(tokens, sn); ok {
				wireOAuth(injector, tokens, sn, srv.Name, rec, log)
			}
		}
	}
}

// wireOAuth installs a refreshing token source for a provider IF a token is stored (else it is not
// yet authorized and stays fail-closed). A refresh persists the new token back to the same shard.
func wireOAuth(injector *secret.Injector, tokens TokenStore, secretName, display string, rec OAuthRecord, log *slog.Logger) {
	raw, ok := tokens.Get(secretName)
	if !ok {
		return
	}
	var tok oauth2.Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		log.Warn("oauth: stored token unreadable", "provider", display, "err", err)
		return
	}
	cfg := oauth.Provider(rec.AuthURL, rec.TokenURL, rec.ClientID, rec.ClientSecret, rec.Scopes...)
	injector.SetResolver(secretName, oauth.NewSource(cfg, &tok, rec.Resource, func(t *oauth2.Token) {
		if err := StoreToken(tokens, secretName, t); err != nil {
			log.Warn("oauth: persist refreshed token", "provider", secretName, "err", err)
		}
	}))
	log.Info("oauth: token source registered", "provider", display)
}

// mcpSecretName derives the owner+host-bound vault key for a server (nil-safe on a bad URL).
func mcpSecretName(srv mcp.Server) (string, bool) {
	u, err := url.Parse(srv.URL)
	if err != nil || u.Host == "" {
		return "", false
	}
	return mcp.SecretName(srv.Name, u.Host), true
}

// providerSuffix names the sibling shard key holding a discovered server's resolved OAuth config,
// next to its token — so the daemon rebuilds the source without re-running discovery at boot.
const providerSuffix = ".provider"

// OAuthRecord is the resolved OAuth config a refreshing source needs: the endpoints (discovered or
// configured), the client (dynamically registered or configured), the RFC 8707 resource, and scopes.
// For a discover-mode server it is persisted at `nocturn auth` time in the server's shard.
type OAuthRecord struct {
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret,omitempty"`
	Resource     string   `json:"resource,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// StoreToken persists an OAuth token as JSON through a TokenStore under name (encrypted at rest in the
// credential's shard). Exported so the CLI auth flow stores through the same routing the daemon uses.
func StoreToken(tokens TokenStore, name string, tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return tokens.Set(name, b)
}

// StoreOAuthRecord persists a discovered server's resolved OAuth config beside its token, so the
// daemon can rebuild the refreshing source at boot without re-discovering.
func StoreOAuthRecord(tokens TokenStore, secretName string, rec OAuthRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return tokens.Set(secretName+providerSuffix, b)
}

// LoadOAuthRecord reads the persisted record, or ok=false if none (server not yet authorized).
func LoadOAuthRecord(tokens TokenStore, secretName string) (OAuthRecord, bool) {
	raw, ok := tokens.Get(secretName + providerSuffix)
	if !ok {
		return OAuthRecord{}, false
	}
	var rec OAuthRecord
	if json.Unmarshal(raw, &rec) != nil {
		return OAuthRecord{}, false
	}
	return rec, true
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
