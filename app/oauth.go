package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/app/mcp"
	"github.com/efuturetoday/nocturn/app/mcp/authflow"
	"github.com/efuturetoday/nocturn/app/secret/oauth"
	"github.com/efuturetoday/nocturn/app/workspace"
)

// runAuth handles `nocturn auth <name>`: unlock the master and connect an OAuth account for the named
// plugin or MCP server, storing the token in that server's folder shard (per-workspace, per-folder
// isolation). Two paths:
//   - a discover-mode MCP server (auth:"oauth") → the full MCP authorization spec: discover the
//     endpoints (RFC 9728 + 8414), dynamically register a client (RFC 7591), then authorize with the
//     RFC 8707 resource indicator. Nothing is hand-configured.
//   - a manual provider (a plugin's oauth block, or an mcp oauth block) → authorize against the
//     configured endpoints.
//
// scopes (from -scope) request specific access; empty lets the authorization server decide. A name
// matching more than one manual provider is ambiguous and refused; qualify with its secretName.
func runAuth(ctx context.Context, name, wsName string, scopes []string) error {
	master, err := openMaster()
	if err != nil {
		return fmt.Errorf("unlock vault: %w", err)
	}
	if master == nil {
		return errors.New("set NOCTURN_MASTER_PASSPHRASE to unlock the vault before connecting an account")
	}
	wsDir := filepath.Join(wsRoot, wsName)
	tokens := workspace.NewShardTokens(master, wsDir, wsName, nil)

	// A discover-mode MCP server named <name> → the spec discovery flow.
	for _, srv := range mcp.Discover(filepath.Join(wsDir, "mcp"), nil).All() {
		if srv.Name == name && srv.OAuthMode() == mcp.AuthDiscover {
			return authDiscover(ctx, tokens, srv, scopes, wsName)
		}
	}

	// Otherwise a manual provider (plugin or mcp oauth block).
	var matches []workspace.OAuthProvider
	for _, p := range workspace.DiscoverOAuth(wsDir) {
		if p.Name == name || p.SecretName == name {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("no OAuth provider named %q in workspace %q (plugins or MCP servers)", name, wsName)
	case 1:
	default:
		ids := make([]string, len(matches))
		for i, p := range matches {
			ids[i] = p.SecretName
		}
		return fmt.Errorf("provider %q is ambiguous in workspace %q — qualify one of: %s", name, wsName, strings.Join(ids, ", "))
	}

	p := matches[0]
	cfg := oauth.Provider(p.AuthURL, p.TokenURL, p.ClientID, p.ClientSecret, p.Scopes...)
	tok, err := oauth.Authorize(ctx, cfg, "", consentPrompt(name, wsName))
	if err != nil {
		return err
	}
	if err := workspace.StoreToken(tokens, p.SecretName, tok); err != nil {
		return fmt.Errorf("store token: %w", err)
	}
	fmt.Printf("connected %q in workspace %q — the daemon will inject and refresh its token.\n", name, wsName)
	return nil
}

// authDiscover runs the spec-driven MCP OAuth flow for a discover-mode server: metadata discovery →
// dynamic client registration → authorization (with the resource indicator) → persist the token and
// the resolved provider record, so the daemon rebuilds the refreshing source without re-discovering.
func authDiscover(ctx context.Context, tokens workspace.ShardTokens, srv mcp.Server, scopes []string, wsName string) error {
	af := authflow.New(nil)

	resource, err := authflow.CanonicalResource(srv.URL)
	if err != nil {
		return err
	}
	u, err := url.Parse(srv.URL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("mcp server %q: bad url", srv.Name)
	}
	secretName := mcp.SecretName(srv.Name, u.Host)

	pr, err := af.ProtectedResourceMetadata(ctx, "", srv.URL)
	if err != nil {
		return fmt.Errorf("discover %q: %w", srv.Name, err)
	}
	as, err := af.AuthorizationServerMetadata(ctx, pr.AuthorizationServers[0])
	if err != nil {
		return fmt.Errorf("discover %q: %w", srv.Name, err)
	}
	if as.RegistrationEndpoint == "" {
		return fmt.Errorf("mcp server %q: its authorization server does not offer dynamic client registration — configure a manual oauth block with a client_id instead", srv.Name)
	}

	// Bind the loopback BEFORE registering: the redirect URI must be the exact callback the flow uses.
	lb, err := oauth.NewLoopback()
	if err != nil {
		return err
	}
	defer lb.Close()

	reg, err := af.Register(ctx, as.RegistrationEndpoint, authflow.RegistrationRequest{
		ClientName:              "nocturn",
		RedirectURIs:            []string{lb.RedirectURL()},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   strings.Join(scopes, " "),
	})
	if err != nil {
		return fmt.Errorf("register client with %q: %w", srv.Name, err)
	}

	cfg := oauth.Provider(as.AuthorizationEndpoint, as.TokenEndpoint, reg.ClientID, reg.ClientSecret, scopes...)
	tok, err := lb.Authorize(ctx, cfg, resource, consentPrompt(srv.Name, wsName))
	if err != nil {
		return err
	}

	if err := workspace.StoreToken(tokens, secretName, tok); err != nil {
		return fmt.Errorf("store token: %w", err)
	}
	if err := workspace.StoreOAuthRecord(tokens, secretName, workspace.OAuthRecord{
		AuthURL: as.AuthorizationEndpoint, TokenURL: as.TokenEndpoint,
		ClientID: reg.ClientID, ClientSecret: reg.ClientSecret, Resource: resource, Scopes: scopes,
	}); err != nil {
		return fmt.Errorf("store provider record: %w", err)
	}
	fmt.Printf("connected %q in workspace %q — the daemon will inject and refresh its token.\n", srv.Name, wsName)
	return nil
}

// consentPrompt prints the authorization URL for the operator to open (no browser exec).
func consentPrompt(name, wsName string) func(string) {
	return func(u string) {
		fmt.Printf("\nOpen this URL to authorize %q (workspace %q), then return here:\n\n  %s\n\n", name, wsName, u)
	}
}
