package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/app/discovery"
	"github.com/efuturetoday/nocturn/app/mcp"
	"github.com/efuturetoday/nocturn/app/plugin"
	"github.com/efuturetoday/nocturn/app/secret"
)

// runSecretSet seeds a static credential value into a plugin/mcp folder's encrypted secret shard.
// The shard is <wsRoot>/<workspace>/<relPath>/secrets.enc, sealed with the key derived from the
// folder's workspace-relative PATH (Master.ShardKey) and AAD bound to that path — so the value is
// cryptographically tied to the folder's placement, exactly like the daemon reads it (LoadShardsInto).
// The value is read from stdin (never argv), so it can be piped from a password manager.
//
// The vault key is DERIVED so the operator never has to spell out the owner-namespacing:
//   - plugins/<name> <cred>  → plugin.SecretName(name, cred)  = "plugin:<name>/<cred>"
//   - mcp/<name>             → mcp.SecretName(name, host)     = "mcp:<name>@<host>/oauth"
//     (a server has one host-bound bearer, so no cred arg; the host comes from its mcp.json)
func runSecretSet(wsName, relPath, cred string) error {
	kind, item, ok := strings.Cut(relPath, "/")
	if !ok || (kind != "plugins" && kind != "mcp") || !discovery.ValidName(item) {
		return fmt.Errorf("relpath must be plugins/<name> or mcp/<name> with a valid name, got %q", relPath)
	}

	master, err := openMaster()
	if err != nil {
		return fmt.Errorf("unlock vault: %w", err)
	}
	if master == nil {
		return errors.New("set NOCTURN_MASTER_PASSPHRASE to unlock the vault before seeding a secret")
	}

	wsDir := filepath.Join(wsRoot, wsName)
	secretKey, err := shardSecretKey(wsDir, kind, item, cred)
	if err != nil {
		return err
	}

	value, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return fmt.Errorf("read value from stdin: %w", err)
	}
	value = bytes.TrimRight(value, "\r\n")
	if len(value) == 0 {
		return errors.New("empty secret value on stdin (pipe the value in, e.g. `printf %s $TOKEN | nocturn secret set ...`)")
	}

	shardDir := filepath.Join(wsDir, relPath)
	if err := os.MkdirAll(shardDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", shardDir, err)
	}
	sv, err := secret.OpenVault(filepath.Join(shardDir, "secrets.enc"), master.ShardKey(wsName, relPath), secret.WithAAD([]byte(relPath)))
	if err != nil {
		return fmt.Errorf("open shard %s: %w", relPath, err)
	}
	if err := sv.Set(secretKey, value); err != nil {
		return fmt.Errorf("store %q: %w", secretKey, err)
	}
	fmt.Printf("stored secret %q into %s (workspace %q)\n", secretKey, relPath, wsName)
	return nil
}

// shardSecretKey builds the owner-namespaced vault key a binding will look the value up under, so it
// always matches what installPlugins / mcp.NewConn register.
func shardSecretKey(wsDir, kind, item, cred string) (string, error) {
	switch kind {
	case "plugins":
		if cred == "" {
			return "", errors.New("plugins/<name> needs a <credential> name (the credential from the plugin's manifest)")
		}
		return plugin.SecretName(item, cred), nil
	case "mcp":
		if cred != "" {
			return "", errors.New("mcp/<name> takes no credential argument — a server has one host-bound bearer")
		}
		srv, ok := mcp.Discover(filepath.Join(wsDir, "mcp"), nil).Get(item)
		if !ok {
			return "", fmt.Errorf("no MCP server %q in workspace (add mcp/%s/mcp.json first)", item, item)
		}
		u, err := url.Parse(srv.URL)
		if err != nil {
			return "", fmt.Errorf("mcp server %q has an unparseable url: %w", item, err)
		}
		return mcp.SecretName(item, u.Host), nil
	default:
		return "", fmt.Errorf("unknown kind %q", kind)
	}
}
