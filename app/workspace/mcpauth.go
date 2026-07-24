package workspace

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/app/mcp"
	"github.com/efuturetoday/nocturn/app/mcp/authflow"
	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/secret/oauth"
)

// This file is the workspace's MCP OAuth orchestration. It splits the spec-driven flow into a
// Begin (discovery + dynamic client registration + consent URL) and a Complete (exchange the code +
// persist), so BOTH drivers can share it: the CLI binds a loopback, gets the URL, catches the
// redirect itself, and Completes in one process; the daemon hands the URL to the companion app over
// the WebSocket and Completes when the app relays the code back from an in-app browser. Because the
// authorization code is single-use and PKCE-bound, the app only ever relays that code — the token is
// minted, held, and refreshed here in the daemon; it never reaches the app, the model, or the guest.

// pendingAuthTTL bounds how long a started authorization may sit before its code comes back. Longer
// than the oauth loopback's own 3-minute wait, so a slow human on the app path is the binding limit.
const pendingAuthTTL = 5 * time.Minute

// MCPAuth owns the in-flight MCP authorization sessions for one workspace. It encapsulates the master
// key, the workspace directory, and the token routing, so neither the CLI nor the daemon's WebSocket
// layer touches secret storage — they carry only an opaque session id and the returned code. Safe for
// concurrent use (the daemon may drive several accounts at once).
type MCPAuth struct {
	wsDir, wsName string
	tokens        TokenStore
	http          *http.Client // discovery + token-exchange transport; nil = the default client

	mu      sync.Mutex
	pending map[string]*pendingAuth
}

// MCPAuthOption configures an MCPAuth at construction.
type MCPAuthOption func(*MCPAuth)

// WithHTTPClient sets the HTTP client used for BOTH the discovery leg and the token exchange — so one
// transport (e.g. a pinned-CA client, or a test server's client) governs the whole flow. Omit it in
// production to use the default client.
func WithHTTPClient(c *http.Client) MCPAuthOption { return func(a *MCPAuth) { a.http = c } }

// pendingAuth is one started-but-unfinished authorization, held between Begin and Complete. It carries
// the PKCE verifier and the resolved config/record; no access token exists yet.
type pendingAuth struct {
	state, verifier, resource string
	secretName, display       string
	cfg                       *oauth2.Config
	record                    OAuthRecord
	expires                   time.Time
}

// NewMCPAuth builds the authorization orchestrator for one workspace. It routes tokens through the
// same per-folder shard the daemon reads at boot, so an account connected here is picked up on the
// next start. log may be nil (it only surfaces a shard read error).
func NewMCPAuth(master *secret.Master, wsDir, wsName string, opts ...MCPAuthOption) *MCPAuth {
	a := &MCPAuth{
		wsDir:   wsDir,
		wsName:  wsName,
		tokens:  NewShardTokens(master, wsDir, wsName, nil),
		pending: map[string]*pendingAuth{},
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// PendingAuth is what a caller needs to drive the redirect step: an opaque id correlating this
// session's Begin and Complete, the consent URL to open, and the redirect prefix the code will arrive
// on — the app watches its in-app browser for a navigation to a URL starting with RedirectPrefix and
// lifts the code+state from its query; the CLI serves that same URL on a loopback.
type PendingAuth struct {
	ID             string
	AuthorizeURL   string
	RedirectPrefix string
}

// NoDynamicRegistrationError reports that a discover-mode server's authorization server does not offer
// RFC 7591 dynamic client registration (GitHub is one such). It carries the discovered endpoints so a
// caller can tell the operator exactly what to put in a manual oauth block instead.
type NoDynamicRegistrationError struct {
	Server, Issuer, AuthURL, TokenURL string
	Scopes                            []string
}

func (e *NoDynamicRegistrationError) Error() string {
	return fmt.Sprintf("mcp server %q: its authorization server (%s) does not offer dynamic client registration", e.Server, e.Issuer)
}

// Begin runs the spec-driven flow up to the consent URL for the named discover-mode server: probe →
// protected-resource metadata → authorization-server metadata → dynamic client registration → build
// the PKCE authorization URL bound to redirectURI. It stashes the PKCE verifier and resolved config
// under an opaque id and returns the URL; nothing is persisted and no token exists until Complete.
// redirectURI is a loopback (CLI) or the app's fixed redirect. It performs network I/O.
func (a *MCPAuth) Begin(ctx context.Context, serverName string, scopes []string, redirectURI string) (PendingAuth, error) {
	a.sweep()

	srv, err := a.discoverServer(serverName)
	if err != nil {
		return PendingAuth{}, err
	}
	resource, err := authflow.CanonicalResource(srv.URL)
	if err != nil {
		return PendingAuth{}, err
	}
	u, err := url.Parse(srv.URL)
	if err != nil || u.Host == "" {
		return PendingAuth{}, fmt.Errorf("mcp server %q: bad url", srv.Name)
	}
	secretName := mcp.SecretName(srv.Name, u.Host)

	af := authflow.New(a.http)
	// Prefer the spec-canonical trigger: an unauthenticated probe returns 401 with the
	// resource_metadata URL; fall back to the well-known location when the server does not.
	metadataURL, _ := af.ProbeResourceMetadata(ctx, srv.URL)
	pr, err := af.ProtectedResourceMetadata(ctx, metadataURL, srv.URL)
	if err != nil {
		return PendingAuth{}, fmt.Errorf("discover %q: %w", srv.Name, err)
	}
	// ProtectedResourceMetadata errors when authorization_servers is empty, so index 0 is safe here.
	as, err := af.AuthorizationServerMetadata(ctx, pr.AuthorizationServers[0])
	if err != nil {
		return PendingAuth{}, fmt.Errorf("discover %q: %w", srv.Name, err)
	}
	if as.RegistrationEndpoint == "" {
		return PendingAuth{}, &NoDynamicRegistrationError{
			Server: srv.Name, Issuer: as.Issuer,
			AuthURL: as.AuthorizationEndpoint, TokenURL: as.TokenEndpoint, Scopes: as.ScopesSupported,
		}
	}
	reg, err := af.Register(ctx, as.RegistrationEndpoint, authflow.RegistrationRequest{
		ClientName:              "nocturn",
		RedirectURIs:            []string{redirectURI},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   strings.Join(scopes, " "),
	})
	if err != nil {
		return PendingAuth{}, fmt.Errorf("register client with %q: %w", srv.Name, err)
	}

	cfg := oauth.Provider(as.AuthorizationEndpoint, as.TokenEndpoint, reg.ClientID, reg.ClientSecret, scopes...)
	state, err := oauth.GenerateState()
	if err != nil {
		return PendingAuth{}, err
	}
	verifier := oauth2.GenerateVerifier()
	authURL := oauth.AuthCodeURL(cfg, state, verifier, resource, redirectURI)

	id, err := oauth.GenerateState()
	if err != nil {
		return PendingAuth{}, err
	}
	a.mu.Lock()
	a.pending[id] = &pendingAuth{
		state: state, verifier: verifier, resource: resource,
		secretName: secretName, display: srv.Name, cfg: cfg,
		record: OAuthRecord{
			AuthURL: as.AuthorizationEndpoint, TokenURL: as.TokenEndpoint,
			ClientID: reg.ClientID, ClientSecret: reg.ClientSecret, Resource: resource, Scopes: scopes,
		},
		expires: time.Now().Add(pendingAuthTTL),
	}
	a.mu.Unlock()

	return PendingAuth{ID: id, AuthorizeURL: authURL, RedirectPrefix: redirectURI}, nil
}

// Complete finishes the session id: verify the returned state against the one Begin stashed, exchange
// the code for a token, and persist the token plus the resolved provider record into the server's
// folder shard. The session is consumed (removed) BEFORE the exchange, so a duplicate callback can
// never mint a second token. On any failure the session stays consumed — the operator restarts with a
// fresh Begin rather than replaying a spent code.
func (a *MCPAuth) Complete(ctx context.Context, id, code, state string) error {
	a.mu.Lock()
	p := a.pending[id]
	delete(a.pending, id)
	a.mu.Unlock()

	if p == nil {
		return fmt.Errorf("auth session %q not found or already used", id)
	}
	if time.Now().After(p.expires) {
		return fmt.Errorf("auth session for %q expired — start again", p.display)
	}
	// security: plain != is fine — state is a public single-use CSRF nonce, not a secret, so there is
	// no timing oracle worth defending (an attacker must forge a whole callback, not guess bytes).
	if state != p.state {
		return errors.New("oauth: state mismatch")
	}
	// oauth2 takes its HTTP client from the context (its own documented contract); hand it the same
	// transport discovery used, so a pinned-CA or test client governs the token exchange too.
	if a.http != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, a.http)
	}
	tok, err := oauth.Exchange(ctx, p.cfg, code, p.verifier, p.resource)
	if err != nil {
		return err
	}
	if err := StoreToken(a.tokens, p.secretName, tok); err != nil {
		return fmt.Errorf("store token: %w", err)
	}
	if err := StoreOAuthRecord(a.tokens, p.secretName, p.record); err != nil {
		return fmt.Errorf("store provider record: %w", err)
	}
	return nil
}

// Account is a connectable MCP account: a discover-mode server and whether it currently holds a token.
type Account struct {
	Server    string `json:"server"`
	Connected bool   `json:"connected"`
}

// List enumerates the workspace's discover-mode MCP servers and whether each already holds a token —
// so a UI can show what can be connected and what already is. Sorted by name for a stable listing.
func (a *MCPAuth) List() []Account {
	var out []Account
	for _, srv := range mcp.Discover(filepath.Join(a.wsDir, "mcp"), nil).All() {
		if srv.OAuthMode() != mcp.AuthDiscover {
			continue
		}
		u, err := url.Parse(srv.URL)
		if err != nil || u.Host == "" {
			continue
		}
		_, connected := a.tokens.Get(mcp.SecretName(srv.Name, u.Host))
		out = append(out, Account{Server: srv.Name, Connected: connected})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Server < out[j].Server })
	return out
}

// discoverServer resolves a discover-mode server by name in this workspace's mcp/ directory. A server
// that exists but is not in discover mode, or is absent, is an error the caller reports.
func (a *MCPAuth) discoverServer(name string) (mcp.Server, error) {
	for _, srv := range mcp.Discover(filepath.Join(a.wsDir, "mcp"), nil).All() {
		if srv.Name != name {
			continue
		}
		if srv.OAuthMode() != mcp.AuthDiscover {
			return mcp.Server{}, fmt.Errorf("mcp server %q is not in discover mode (needs auth: \"oauth\")", name)
		}
		return srv, nil
	}
	return mcp.Server{}, fmt.Errorf("no discover-mode MCP server named %q in this workspace", name)
}

// sweep drops expired sessions so an abandoned Begin cannot leak. Called on each Begin — no background
// goroutine, so MCPAuth needs no lifecycle management.
func (a *MCPAuth) sweep() {
	now := time.Now()
	a.mu.Lock()
	for id, p := range a.pending {
		if now.After(p.expires) {
			delete(a.pending, id)
		}
	}
	a.mu.Unlock()
}
