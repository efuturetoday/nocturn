package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
func runAuth(ctx context.Context, name, wsName string, scopes []string, clientID, clientSecret string) error {
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
		if p.Name == name || p.SecretName == name || ownedBy(p.SecretName, name) {
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
	rec, err := resolveClient(tokens, p, clientID, clientSecret)
	if err != nil {
		return err
	}
	cfg := oauth.Provider(rec.AuthURL, rec.TokenURL, rec.ClientID, rec.ClientSecret, rec.Scopes...)
	tok, err := oauth.Authorize(ctx, cfg, "", consentPrompt(name, wsName))
	if err != nil {
		return err
	}
	if err := workspace.StoreToken(tokens, p.SecretName, tok); err != nil {
		return fmt.Errorf("store token: %w", err)
	}
	// The client goes beside the token, in the same shard, so the daemon can refresh without the
	// manifest carrying an OAuth client it cannot have. Written AFTER the token: a record without a
	// token is a provider that looks configured and is not, which is the confusing half of the pair.
	if err := workspace.StoreOAuthRecord(tokens, p.SecretName, rec); err != nil {
		return fmt.Errorf("store client: %w", err)
	}
	fmt.Printf("connected %q in workspace %q.\n", name, wsName)
	// A running daemon registered its token sources during its last discovery pass, and this ran in a
	// different process: without a reload it keeps failing with "not connected", the person re-runs
	// this command, sees success, and fails again — a loop with no exit visible from either side.
	nudgeDaemon(wsName)
	return nil
}

// nudgeDaemon asks a running daemon to re-read the workspace, so a token stored a second ago is
// actually usable. Best-effort by design: authorizing an account with no daemon running is a normal
// thing to do (it is how one is set up before the first start), so a daemon that is not there is not
// an error — it will read the shard when it opens the workspace.
func nudgeDaemon(wsName string) {
	if code := cmdReload(":8080", wsName); code != 0 {
		fmt.Println("the daemon did not pick it up; it will on the next start, or run: nocturn reload")
	}
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

// readClientSecret reads the OAuth client secret from stdin when asked for it.
//
// A flag would put it in the shell history and in every `ps` on the machine, which is the reason
// `nocturn secret set` has taken its value on stdin from the start. A client secret is no less of a
// secret for being called a client one — for a desktop client Google itself calls it
// non-confidential, and that is a claim about ITS threat model, not about yours.
//
// A secret with no --client-id is refused rather than ignored: it would otherwise be read, dropped,
// and the command would report success.
func readClientSecret(fromStdin bool, clientID string) (string, error) {
	if !fromStdin {
		return "", nil
	}
	if clientID == "" {
		return "", errors.New("-client-secret-stdin without -client-id: a secret alone identifies no client")
	}
	value, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read the client secret from stdin: %w", err)
	}
	secret := strings.TrimRight(string(value), "\r\n")
	if secret == "" {
		return "", errors.New("empty client secret on stdin (pipe it in, e.g. `printf %s \"$SECRET\" | nocturn auth gmail -client-id … -client-secret-stdin`)")
	}
	return secret, nil
}

// ownedBy reports whether an owner-namespaced secret belongs to the plugin or server called name.
//
// It exists because a provider's own Name is the name of its OAUTH BLOCK, not of the thing that
// declared it — a plugin's block is called "account" or "token", because it has to match a credential
// of the same name. So `nocturn auth gmail` matched nothing while every document, the plugin's own
// error message and its guide all say to type exactly that. Matching the owner is what a person means:
// they name the plugin they installed, not a field inside its manifest.
func ownedBy(secretName, name string) bool {
	rest, ok := strings.CutPrefix(secretName, "plugin:")
	if !ok {
		return false
	}
	owner, _, _ := strings.Cut(rest, "/")
	return owner == name
}

// resolveClient decides which OAuth client this authorization runs with, in one place.
//
// Three sources, in the order that matches how a person arrives here. The FLAGS win, because typing
// one is how somebody replaces a client that was rotated or wrong. Then a client stored by an earlier
// `nocturn auth` — re-authorizing must not demand the id again. Then the manifest, which is where a
// plugin for a provider with an ordinary public client carries its own.
//
// Nothing left means a refusal with the flag named, and that refusal is the whole reason this
// function exists: a catalog plugin for Gmail cannot ship a client id (a shared one would need
// Google's annual third-party security assessment for a restricted scope), so the shipped manifest
// leaves it empty and the person supplies theirs once.
func resolveClient(tokens workspace.TokenStore, p workspace.OAuthProvider, clientID, clientSecret string) (workspace.OAuthRecord, error) {
	rec := workspace.OAuthRecord{
		AuthURL:      p.AuthURL,
		TokenURL:     p.TokenURL,
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Scopes:       p.Scopes,
	}
	if stored, ok := workspace.LoadOAuthRecord(tokens, p.SecretName); ok && stored.ClientID != "" {
		rec.ClientID, rec.ClientSecret = stored.ClientID, stored.ClientSecret
	}
	if clientID != "" {
		// The secret follows the id: replacing the client means replacing both, and carrying the old
		// secret under a new id would authenticate as neither. Passing the SAME id again with no
		// secret is somebody re-running the command, so what was stored survives.
		if clientID != rec.ClientID || clientSecret != "" {
			rec.ClientSecret = clientSecret
		}
		rec.ClientID = clientID
	}
	if rec.ClientID == "" {
		return workspace.OAuthRecord{}, fmt.Errorf(
			"%q ships no OAuth client — register one with the provider and pass it once:\n"+
				"    nocturn auth %s --client-id <id> [--client-secret <secret>]", p.Name, p.Name)
	}
	return rec, nil
}

// consentPrompt prints the authorization URL for the operator to open (no browser exec).
func consentPrompt(name, wsName string) func(string) {
	return func(u string) {
		fmt.Printf("\nOpen this URL to authorize %q (workspace %q), then return here:\n\n  %s\n\n", name, wsName, u)
	}
}
