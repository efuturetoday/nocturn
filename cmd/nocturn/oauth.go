package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/secret/oauth"
	"github.com/efuturetoday/nocturn/internal/workspace"
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
			return authDiscover(ctx, master, wsDir, wsName, name, scopes)
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

// authDiscover runs the spec-driven MCP OAuth flow for a discover-mode server through the SAME
// workspace orchestration the companion app drives (workspace.MCPAuth): Begin does discovery +
// dynamic registration + the consent URL, the loopback here catches the redirect, and Complete
// exchanges the code and persists the token + provider record into the server's folder shard. The
// only CLI-specific parts are binding the loopback and printing the URL; the app supplies its own
// redirect and relays the code instead.
func authDiscover(ctx context.Context, master *secret.Master, wsDir, wsName, name string, scopes []string) error {
	auth := workspace.NewMCPAuth(master, wsDir, wsName)

	// Bind the loopback BEFORE Begin: its redirect URI is what Begin registers with the server.
	lb, err := oauth.NewLoopback()
	if err != nil {
		return err
	}
	defer lb.Close()

	p, err := auth.Begin(ctx, name, scopes, lb.RedirectURL())
	if err != nil {
		var nd *workspace.NoDynamicRegistrationError
		if errors.As(err, &nd) {
			// The authorization server wants a pre-registered OAuth App (GitHub is one such). Print the
			// discovered endpoints so the operator only registers an app, gets a client_id, and drops
			// a manual oauth block into mcp/<name>/mcp.json.
			return fmt.Errorf("%w.\nRegister an OAuth app there, then replace auth:\"oauth\" with this "+
				"block in mcp/%s/mcp.json and your client_id:\n\n  \"oauth\": {\n    \"auth_url\": %q,\n    \"token_url\": %q,\n    \"client_id\": \"<your client id>\",\n    \"scopes\": %v\n  }\n\n(or just use a token: `nocturn secret set mcp:%s`)",
				nd, nd.Server, nd.AuthURL, nd.TokenURL, nd.Scopes, nd.Server)
		}
		return err
	}

	consentPrompt(name, wsName)(p.AuthorizeURL)
	code, state, err := lb.WaitForCode(ctx)
	if err != nil {
		return err
	}
	if err := auth.Complete(ctx, p.ID, code, state); err != nil {
		return err
	}
	fmt.Printf("connected %q in workspace %q — the daemon will inject and refresh its token.\n", name, wsName)
	return nil
}

// consentPrompt prints the authorization URL for the operator to open (no browser exec).
func consentPrompt(name, wsName string) func(string) {
	return func(u string) {
		fmt.Printf("\nOpen this URL to authorize %q (workspace %q), then return here:\n\n  %s\n\n", name, wsName, u)
	}
}
