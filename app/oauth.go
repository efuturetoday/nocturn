package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/app/secret/oauth"
	"github.com/efuturetoday/nocturn/app/workspace"
)

// runAuth handles `nocturn auth <name> [workspace]`: unlock the master, open the TARGET workspace's
// own vault, find the OAuth provider named <name> among that workspace's plugins and MCP servers, run
// the interactive authorization-code (+PKCE) flow, and store the token in that vault — so it injects
// only for that workspace (per-workspace isolation). The consent URL prints to the terminal today;
// the same prompt seam will drive the companion app once it replaces the terminal.
//
// A bare name that matches more than one provider (a plugin and an MCP server sharing it) is
// ambiguous and refused; qualify with the exact owner-namespaced secretName. Discovery + the vault
// key derivation live in app/workspace (and the source packages) — this flow only drives the browser
// and stores the result.
func runAuth(ctx context.Context, name, wsName string) error {
	master, err := openMaster()
	if err != nil {
		return fmt.Errorf("unlock vault: %w", err)
	}
	if master == nil {
		return errors.New("set NOCTURN_MASTER_PASSPHRASE to unlock the vault before connecting an account")
	}
	wsDir := filepath.Join(wsRoot, wsName)

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
		// authorize below
	default:
		ids := make([]string, len(matches))
		for i, p := range matches {
			ids[i] = p.SecretName
		}
		return fmt.Errorf("provider %q is ambiguous in workspace %q — qualify one of: %s", name, wsName, strings.Join(ids, ", "))
	}

	p := matches[0]
	cfg := oauth.Provider(p.AuthURL, p.TokenURL, p.ClientID, p.ClientSecret, p.Scopes...)
	tok, err := oauth.Authorize(ctx, cfg, "", func(u string) {
		fmt.Printf("\nOpen this URL to authorize %q (workspace %q), then return here:\n\n  %s\n\n", name, wsName, u)
	})
	if err != nil {
		return err
	}
	// The token lands in the owning plugin/mcp folder's shard (path-encrypted), not the workspace
	// vault — the same routing the daemon reads it back through.
	if err := workspace.StoreToken(workspace.NewShardTokens(master, wsDir, wsName, nil), p.SecretName, tok); err != nil {
		return fmt.Errorf("store token: %w", err)
	}
	fmt.Printf("connected %q in workspace %q — the daemon will inject and refresh its token.\n", name, wsName)
	return nil
}
