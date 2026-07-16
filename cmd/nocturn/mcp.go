package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/internal/approval"
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
func loadMCP(ctx context.Context, reg *tool.Registry, guard *gateway.Guard, inj *secret.Injector, scanner *secret.Scanner, vault *secret.Vault, approvals *approval.Store, wsDir string) error {
	servers, err := mcpcap.LoadConfig(filepath.Join(wsDir, "mcp.json"))
	if err != nil || len(servers) == 0 {
		return err
	}
	// Own client, longer than netcap's 15 s: an MCP tools/call may stream its
	// response over SSE. The Timeout still bounds the WHOLE exchange including
	// the body read, so a stalling server cannot hang a tool call forever.
	httpClient := &http.Client{Timeout: 60 * time.Second}
	for _, srv := range servers {
		// An unchanged server (same name/url/auth as last approved) connects
		// silently; a new or changed one is reviewed (with a diff) and recorded.
		content, err := json.Marshal(srv)
		if err != nil {
			return err
		}
		if ok, prior := approvals.Status("mcp", srv.Name, content); !ok {
			if prior != nil {
				fmt.Printf("\n⚠  MCP server %q changed since you last approved it:\n", srv.Name)
				printApprovalDiff(prior, content)
			}
			if !reviewMCP(srv) {
				fmt.Printf("MCP server %q skipped.\n", srv.Name)
				continue
			}
			if err := approvals.Approve("mcp", srv.Name, content); err != nil {
				return err
			}
		}
		conn, err := mcpcap.New(srv, guard, inj, scanner, httpClient)
		if err != nil {
			return err
		}
		if err := wireMCPCredential(ctx, inj, vault, srv, conn.Host()); err != nil {
			return err
		}
		if err := establishMCP(ctx, conn, srv, reg, inj, vault); err != nil {
			// A flaky or offline remote must NEVER brick startup (or an agent run):
			// skip this server with a notice and keep the assistant running with the
			// tools that ARE reachable. (Lazy connect + schema cache — FRAGEN #5 — is
			// the fuller resilience; this is the no-brick floor.)
			fmt.Printf("MCP server %q skipped — could not connect: %v\n", srv.Name, err)
			continue
		}
	}
	return nil
}

// establishMCP performs the handshake and registers the server's tools. Every
// failure — an unreachable/offline server, a rejected or malformed handshake, a
// tool-name collision — is RETURNED (not fatal), so loadMCP skips just this
// server. The credential re-entry on a server rejection still happens here.
func establishMCP(ctx context.Context, conn *mcpcap.Conn, srv mcpcap.Server, reg *tool.Registry, inj *secret.Injector, vault *secret.Vault) error {
	// The operator's approval IS the human ok for the two setup calls (initialize +
	// tools/list): an ephemeral grant for exactly (http.write, <host>), carried ONLY
	// by this setup context — runtime tool calls still ask (the turn ctx never sees it).
	// The setup calls (Connect/Tools) run OUTSIDE the tool registry, so their gate
	// sees no outermost tool name — record the ephemeral grant under tool "" to
	// match, scoped to exactly (http.write, <host>) for this setup context only.
	setup := capability.NewGrants(capability.Permanent, nil)
	_ = setup.Record("", capability.Call{Family: "http", Write: true, Target: conn.Host()}, capability.ScopeSession)
	setupCtx := capability.WithGrants(ctx, setup)

	if err := conn.Connect(setupCtx); err != nil {
		// A non-2xx (StatusError) is a plausible bad/expired/revoked token — offer to
		// re-enter it and retry once. A network failure leaves the token untouched.
		retried, rerr := reenterOnRejection(ctx, inj, vault, srv, conn.Host(), err)
		if rerr != nil {
			return rerr
		}
		if !retried {
			return err
		}
		if err := conn.Connect(setupCtx); err != nil {
			return err
		}
	}
	tools, err := conn.Tools(setupCtx)
	if err != nil {
		return err
	}
	for _, t := range tools { // reject collisions before touching anything
		if reg.Has(t.Name) {
			return fmt.Errorf("tool %q collides with an existing tool", t.Name)
		}
	}
	for _, t := range tools {
		reg.Add(t)
	}
	fmt.Printf("MCP server %q connected — %d tool(s) registered.\n", srv.Name, len(tools))
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
	if srv.Auth == "token" {
		fmt.Print("    token  static bearer — you'll be prompted once; stored encrypted in the vault, injected host-side\n")
	}
	fmt.Printf("    Its tools join the registry as %q; every call is a gated http.write to its host.\n", srv.Name+".*")
	fmt.Print("Connect? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

// wireMCPCredential supplies the value behind the connection's owner-namespaced
// credential binding (added by mcpcap.New). For auth "token" the host prompts
// once (no echo) and stores the entered Bearer in the encrypted vault under the
// binding's secret name — never from mcp.json or the environment, so nothing
// secret leaks and the config stays committable; a token already in the vault
// (a later run) skips the prompt. An OAuth declaration runs the provider flow
// (mirroring wirePluginOAuth): build the config from its endpoints, load-or-
// authorize a token (kept in the vault, keyed per server), and register a
// refreshing Bearer source — so the injector stamps it, host-side, only onto
// this connection's requests. Neither the model nor the server config chooses
// the header; mcpcap.New bound it at construction. Validate made auth/oauth
// mutually exclusive.
func wireMCPCredential(ctx context.Context, inj *secret.Injector, vault *secret.Vault, srv mcpcap.Server, host string) error {
	if inj == nil {
		return nil
	}
	// Same host-bound key the binding from mcpcap.New resolves, so only THIS
	// connection's binding can reach this source — never another owner's, and
	// never the token issued for a DIFFERENT host under the same server name.
	name := mcpcap.SecretName(srv.Name, host)
	if srv.Auth == "token" {
		// A CLEAN token already in the vault (an earlier run) → no prompt. The
		// stored value is injected verbatim as "Bearer <v>", so anything but a
		// bare, whitespace-free token counts as absent and re-prompts (then
		// overwrites) — otherwise a stray space/newline yields "Bearer …\n", which
		// the server rejects as a badly formatted header (HTTP 400).
		if v, ok := vault.Get(name); ok && validBearer(string(v)) {
			return nil
		}
		token, err := readPassphrase(fmt.Sprintf("Bearer token for MCP server %q: ", srv.Name))
		if err != nil {
			return fmt.Errorf("mcp %s: read token: %w", srv.Name, err)
		}
		token = strings.TrimSpace(token)
		if !validBearer(token) {
			return fmt.Errorf("mcp %s: token must be non-empty and contain no whitespace", srv.Name)
		}
		if err := vault.Set(name, []byte(token)); err != nil {
			return fmt.Errorf("mcp %s: persist token: %w", srv.Name, err)
		}
		return nil
	}
	if srv.OAuth == nil {
		return nil
	}
	o := srv.OAuth
	cfg := oauth.Provider(o.AuthURL, o.TokenURL, o.ClientID, o.ClientSecret, o.Scopes...)
	tok, ok := vaultToken(vault, name)
	if !ok {
		fmt.Printf("\nMCP server %q needs authorization (scopes: %s)\n", srv.Name, strings.Join(o.Scopes, " "))
		var err error
		if tok, err = oauth.Authorize(ctx, cfg, nil); err != nil { // nil prompt = print the URL
			return fmt.Errorf("mcp %s: authorize: %w", srv.Name, err)
		}
		if err := saveVaultToken(vault, name, tok); err != nil {
			return fmt.Errorf("mcp %s: persist token: %w", srv.Name, err)
		}
	}
	inj.SetResolver(name, oauth.NewCredential(cfg, tok, persistToken(vault, name)))
	return nil
}

// validBearer reports whether s is usable verbatim as a Bearer credential: a
// non-empty token with no surrounding or internal whitespace. Any space or
// newline would produce a malformed "Authorization: Bearer …" header, which
// servers reject with HTTP 400.
func validBearer(s string) bool {
	return s != "" && s == strings.TrimSpace(s) && !strings.ContainsAny(s, " \t\r\n\f\v")
}

// reenterOnRejection recovers from a rejected credential at connect time WITHOUT
// purging the vault: a stored token that is bad, expired, revoked, rotated, or
// (as here) a leftover placeholder must be fixable one credential at a time.
// It acts ONLY on a server rejection (a non-2xx StatusError — the server was
// reached); a network/transport failure leaves the credential untouched. For a
// static token it re-prompts (no echo) and overwrites the vault entry; for OAuth
// it re-runs the authorization flow and swaps in the fresh refreshing source.
// Returns whether a retry is worthwhile (a new credential was stored).
func reenterOnRejection(ctx context.Context, inj *secret.Injector, vault *secret.Vault, srv mcpcap.Server, host string, cause error) (bool, error) {
	if !mcpcap.IsServerRejection(cause) {
		return false, nil // no response from the server — not a credential problem
	}
	name := mcpcap.SecretName(srv.Name, host)
	switch {
	case srv.Auth == "token":
		fmt.Printf("\nMCP server %q rejected the stored token (%v).\n", srv.Name, cause)
		if !askYesNo("Re-enter the bearer token?") {
			return false, nil
		}
		token, err := readPassphrase(fmt.Sprintf("Bearer token for MCP server %q: ", srv.Name))
		if err != nil {
			return false, fmt.Errorf("mcp %s: read token: %w", srv.Name, err)
		}
		token = strings.TrimSpace(token)
		if !validBearer(token) {
			return false, fmt.Errorf("mcp %s: token must be non-empty and contain no whitespace", srv.Name)
		}
		if err := vault.Set(name, []byte(token)); err != nil {
			return false, fmt.Errorf("mcp %s: persist token: %w", srv.Name, err)
		}
		return true, nil
	case srv.OAuth != nil:
		fmt.Printf("\nMCP server %q rejected the stored OAuth token (%v).\n", srv.Name, cause)
		if !askYesNo("Re-authorize now?") {
			return false, nil
		}
		o := srv.OAuth
		cfg := oauth.Provider(o.AuthURL, o.TokenURL, o.ClientID, o.ClientSecret, o.Scopes...)
		tok, err := oauth.Authorize(ctx, cfg, nil)
		if err != nil {
			return false, fmt.Errorf("mcp %s: authorize: %w", srv.Name, err)
		}
		if err := saveVaultToken(vault, name, tok); err != nil {
			return false, fmt.Errorf("mcp %s: persist token: %w", srv.Name, err)
		}
		inj.SetResolver(name, oauth.NewCredential(cfg, tok, persistToken(vault, name)))
		return true, nil
	}
	return false, nil
}

// askYesNo prints a [y/N] prompt on the plain terminal (pre-TUI) and reports
// whether the operator answered yes.
func askYesNo(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}
