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
// The target is the owner-namespaced credential the value belongs to — the SAME identifier that shows
// up in `secret ls`, diagnostics, and the vault:
//
//	plugin:<name>/<credential>   a plugin credential (from the plugin's manifest)
//	mcp:<name>                   an MCP server's bearer (host-bound; the host comes from its mcp.json)
//
// The value is read from stdin (never argv), so it can be piped from a password manager. It is sealed
// into <wsRoot>/<workspace>/<relPath>/secrets.enc under the folder-path-derived key + path-bound AAD —
// exactly the shard the daemon reads at startup (LoadShardsInto).
func runSecretSet(wsName, target string) error {
	master, err := openMaster()
	if err != nil {
		return fmt.Errorf("unlock vault: %w", err)
	}
	if master == nil {
		return errors.New("set NOCTURN_MASTER_PASSPHRASE to unlock the vault before seeding a secret")
	}

	wsDir := filepath.Join(wsRoot, wsName)
	relPath, secretKey, err := resolveSecretTarget(wsDir, target)
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
	fmt.Printf("stored %s in workspace %q\n", secretKey, wsName)
	return nil
}

// resolveSecretTarget maps an owner-form target to its shard folder (relPath) and the vault key a
// binding looks the value up under, so a seeded value always lands under exactly the key
// installPlugins / mcp.NewConn register.
func resolveSecretTarget(wsDir, target string) (relPath, key string, err error) {
	switch {
	case strings.HasPrefix(target, "plugin:"):
		name, cred, ok := strings.Cut(strings.TrimPrefix(target, "plugin:"), "/")
		if !ok || !discovery.ValidName(name) || cred == "" {
			return "", "", fmt.Errorf("plugin target must be plugin:<name>/<credential>, got %q", target)
		}
		return "plugins/" + name, plugin.SecretName(name, cred), nil

	case strings.HasPrefix(target, "mcp:"):
		name := strings.TrimPrefix(target, "mcp:")
		if !discovery.ValidName(name) {
			return "", "", fmt.Errorf("mcp target must be mcp:<name>, got %q", target)
		}
		srv, ok := mcp.Discover(filepath.Join(wsDir, "mcp"), nil).Get(name)
		if !ok {
			return "", "", fmt.Errorf("no MCP server %q in workspace (add mcp/%s/mcp.json first)", name, name)
		}
		u, err := url.Parse(srv.URL)
		if err != nil {
			return "", "", fmt.Errorf("mcp server %q has an unparseable url: %w", name, err)
		}
		return "mcp/" + name, mcp.SecretName(name, u.Host), nil

	default:
		return "", "", fmt.Errorf("target must be plugin:<name>/<credential> or mcp:<name>, got %q", target)
	}
}
