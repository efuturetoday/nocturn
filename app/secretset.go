package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/app/discovery"
	"github.com/efuturetoday/nocturn/app/secret"
)

// runSecretSet seeds a static credential value into a plugin/mcp folder's encrypted secret shard.
// The shard is <wsRoot>/<workspace>/<relPath>/secrets.enc, sealed with the key derived from the
// folder's workspace-relative PATH (Master.ShardKey) and AAD bound to that path — so the value is
// cryptographically tied to the folder's placement, exactly like the daemon reads it (LoadShardsInto).
// The value is read from stdin (never argv), so it can be piped from a password manager. secretKey is
// the credential's owner-namespaced name — plugin.SecretName / mcp.SecretName — e.g. plugin:gmail/acct.
func runSecretSet(wsName, relPath, secretKey string) error {
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

	value, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return fmt.Errorf("read value from stdin: %w", err)
	}
	value = bytes.TrimRight(value, "\r\n")
	if len(value) == 0 {
		return errors.New("empty secret value on stdin (pipe the value in, e.g. `printf %s $TOKEN | nocturn secret set ...`)")
	}

	shardDir := filepath.Join(wsRoot, wsName, relPath)
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
