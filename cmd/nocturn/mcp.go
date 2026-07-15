package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/mcpcap"
	"github.com/efuturetoday/nocturn/internal/oauth"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// loadMCP connects the remote MCP servers declared in the workspace
// control-plane (<ws>/mcp.json — host-managed, outside the model's
// mount, ADR-10) into the shared registry. Each connection is reviewed on
// stdin BEFORE the TUI (server URL + scopes, y/N — like a plugin install);
// a declined server is skipped, not fatal. Run before bubbletea grabs the
// terminal: the review prompt AND any OAuth consent URL use stdin/stdout.
// It is a no-op when no config exists.
func loadMCP(ctx context.Context, reg *tool.Registry, guard *gateway.Guard, inj *secret.Injector, scanner *secret.Scanner, wsDir string) error {
	servers, err := mcpcap.LoadConfig(filepath.Join(wsDir, "mcp.json"))
	if err != nil || len(servers) == 0 {
		return err
	}
	// Own client, longer than netcap's 15 s: an MCP tools/call may stream its
	// response over SSE. The Timeout still bounds the WHOLE exchange including
	// the body read, so a stalling server cannot hang a tool call forever.
	httpClient := &http.Client{Timeout: 60 * time.Second}
	for _, srv := range servers {
		if !reviewMCP(srv) {
			fmt.Printf("MCP server %q skipped.\n", srv.Name)
			continue
		}
		conn, err := mcpcap.New(srv, guard, inj, scanner, httpClient)
		if err != nil {
			return err
		}
		if err := wireMCPOAuth(ctx, inj, srv); err != nil {
			return err
		}
		// The operator's "y" IS the human approval for the two setup calls
		// (initialize + tools/list): a grant for exactly (http.write, <host>),
		// carried ONLY by this setup context — the TUI notifier is not running
		// yet, and runtime tool calls still ask (the turn ctx never sees this
		// grant set).
		setup := capability.NewGrants("mcp-setup:"+srv.Name, capability.Permanent, nil)
		_ = setup.Record(capability.Call{Capability: "http.write", Target: conn.Host()}, capability.ScopeSession)
		setupCtx := capability.WithGrants(ctx, setup)

		if err := conn.Connect(setupCtx); err != nil {
			return err
		}
		tools, err := conn.Tools(setupCtx)
		if err != nil {
			return err
		}
		for _, t := range tools { // reject collisions before touching anything
			if reg.Has(t.Name) {
				return fmt.Errorf("mcp %s: tool %q collides with an existing tool", srv.Name, t.Name)
			}
		}
		for _, t := range tools {
			reg.Add(t)
		}
		fmt.Printf("MCP server %q connected — %d tool(s) registered.\n", srv.Name, len(tools))
	}
	return nil
}

// reviewMCP shows what connecting means — the server URL its tools will talk
// to, and the OAuth scopes whose token will ride along — and asks the operator
// to confirm. This is the ONE trust decision for the connection; per-call asks
// still happen at runtime.
func reviewMCP(srv mcpcap.Server) bool {
	fmt.Printf("\nConnect to remote MCP server %q?\n", srv.Name)
	fmt.Printf("    url    %s\n", srv.URL)
	if o := srv.OAuth; o != nil {
		fmt.Printf("    oauth  %s (scopes: %s)\n", o.AuthURL, strings.Join(o.Scopes, " "))
	}
	fmt.Printf("    Its tools join the registry as %q; every call is a gated http.write to its host.\n", srv.Name+".*")
	fmt.Print("Connect? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

// wireMCPOAuth runs the server's config-declared OAuth provider (mirroring
// wirePluginOAuth): build the config from its endpoints, load-or-authorize a
// token (persisted per server), and register a refreshing Bearer source under
// the connection's owner-namespaced credential — so the injector stamps it,
// host-side, only onto this connection's requests. Neither the model nor the
// server config chooses the header; mcpcap.New bound it at construction.
func wireMCPOAuth(ctx context.Context, inj *secret.Injector, srv mcpcap.Server) error {
	if srv.OAuth == nil || inj == nil {
		return nil
	}
	o := srv.OAuth
	cfg := oauth.Provider(o.AuthURL, o.TokenURL, o.ClientID, o.ClientSecret, o.Scopes...)
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "nocturn", "oauth", "mcp-"+srv.Name+".json")
	tok, ok := loadTokenAt(path)
	if !ok {
		fmt.Printf("\nMCP server %q needs authorization (scopes: %s)\n", srv.Name, strings.Join(o.Scopes, " "))
		if tok, err = oauth.Authorize(ctx, cfg, nil); err != nil { // nil prompt = print the URL
			return fmt.Errorf("mcp %s: authorize: %w", srv.Name, err)
		}
		if err := saveTokenAt(path, tok); err != nil {
			return fmt.Errorf("mcp %s: persist token: %w", srv.Name, err)
		}
	}
	p := path
	// Same namespaced key the binding from mcpcap.New resolves, so only THIS
	// connection's binding can reach this source — never another owner's.
	inj.SetResolver(mcpcap.SecretName(mcpcap.Owner(srv.Name), mcpcap.CredentialName),
		oauth.NewCredential(cfg, tok, func(t *oauth2.Token) { _ = saveTokenAt(p, t) }))
	return nil
}
