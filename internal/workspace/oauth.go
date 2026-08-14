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

	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/mcp/authflow"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/secret/oauth"
)

// This file is where a workspace's OAuth credentials live: which providers it exposes, where their
// tokens are stored (each in its owner's folder shard, never the workspace vault), and how a stored
// token becomes a refreshing source on the injector.
//
// It is separate from secrets.go because the two answer different questions. That file assembles the
// vault and the resolution store — the durable stack, built once. This one is discovery-shaped: which
// plugins and MCP servers exist right now decides which providers there are, which is why
// registerOAuth runs on every discovery pass rather than only at startup.

// providerSuffix names the sibling shard key holding a discovered server's resolved OAuth config,
// next to its token — so the daemon rebuilds the source without re-running discovery at boot.
const providerSuffix = ".provider"

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
	reg := registrar{injector: injector, tokens: tokens, log: log.With("component", "oauth")}
	// Plugin OAuth: endpoints from the manifest, no resource indicator (not an MCP resource). A client
	// stored at `nocturn auth` time WINS over the manifest's, because a plugin from the catalog cannot
	// carry one — a shared OAuth client for a restricted scope like Gmail needs an annual third-party
	// security assessment, so the shipped manifest leaves the client empty and the person supplies
	// theirs once. Same shard, same sidecar record a discover-mode MCP server uses.
	for _, p := range plugin.DiscoverOAuth(wsDir) {
		reg.wire(p.SecretName, p.Name, pluginRecord(p, tokens))
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
			reg.wire(sn, srv.Name, OAuthRecord{
				AuthURL:      srv.OAuth.AuthURL,
				TokenURL:     srv.OAuth.TokenURL,
				ClientID:     srv.OAuth.ClientID,
				ClientSecret: srv.OAuth.ClientSecret,
				Resource:     resource,
				Scopes:       srv.OAuth.Scopes,
			})
		case mcp.AuthDiscover:
			if rec, ok := LoadOAuthRecord(tokens, sn); ok {
				reg.wire(sn, srv.Name, rec)
			}
		}
	}
}

// pluginRecord resolves which OAuth client a plugin refreshes with.
//
// The ENDPOINTS always come from the manifest, which is signed: where a credential is sent is not
// something a stored blob should be able to move. The CLIENT may come from the shard, because a
// plugin from the catalog often cannot ship one — a shared OAuth client for a restricted scope like
// Gmail needs an annual third-party security assessment, so the manifest leaves it empty and the
// person supplies theirs once with `nocturn auth <plugin> --client-id …`, which stores it beside the
// token. A stored record without a client id changes nothing, so a half-written one cannot blank out
// a manifest that does carry one.
func pluginRecord(p plugin.OAuthProvider, tokens TokenStore) OAuthRecord {
	rec := OAuthRecord{
		AuthURL:      p.AuthURL,
		TokenURL:     p.TokenURL,
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Scopes:       p.Scopes,
	}
	if stored, ok := LoadOAuthRecord(tokens, p.SecretName); ok && stored.ClientID != "" {
		rec.ClientID, rec.ClientSecret = stored.ClientID, stored.ClientSecret
	}
	return rec
}

// registrar carries the three things every provider in one registration run shares — where to install
// the source, where its token lives, and where to say so. Grouping them is what keeps wire's own
// signature about the PROVIDER: which credential, what to call it, and its resolved config.
type registrar struct {
	injector *secret.Injector
	tokens   TokenStore
	log      *slog.Logger
}

// wire installs a refreshing token source for one provider IF a token is stored (else it is not yet
// authorized and stays fail-closed). A refresh persists the new token back to the same shard.
func (r registrar) wire(secretName, display string, rec OAuthRecord) {
	raw, ok := r.tokens.Get(secretName)
	if !ok {
		return
	}
	var tok oauth2.Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		r.log.Warn("oauth: stored token unreadable", "provider", display, "err", err)
		return
	}
	cfg := oauth.Provider(rec.AuthURL, rec.TokenURL, rec.ClientID, rec.ClientSecret, rec.Scopes...)
	r.injector.SetResolver(secretName, oauth.NewSource(cfg, &tok, rec.Resource, func(t *oauth2.Token) {
		if err := StoreToken(r.tokens, secretName, t); err != nil {
			r.log.Warn("oauth: persist refreshed token", "provider", secretName, "err", err)
		}
	}))
	r.log.Info("oauth: token source registered", "provider", display)
}

// mcpSecretName derives the owner+host-bound vault key for a server (nil-safe on a bad URL).
func mcpSecretName(srv mcp.Server) (string, bool) {
	u, err := url.Parse(srv.URL)
	if err != nil || u.Host == "" {
		return "", false
	}
	return mcp.SecretName(srv.Name, u.Host), true
}

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
