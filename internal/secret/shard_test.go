package secret_test

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/secret"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func anyName(string) bool { return true }

// writeShard seals one secret into <wsDir>/<relPath>/secrets.enc under the path-derived key.
func writeShard(t *testing.T, m *secret.Master, wsDir, wsName, relPath, key, value string) {
	t.Helper()
	dir := filepath.Join(wsDir, relPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	v, err := secret.OpenVault(filepath.Join(dir, "secrets.enc"), m.ShardKey(wsName, relPath), secret.WithAAD([]byte(relPath)))
	if err != nil {
		t.Fatalf("open shard %s: %v", relPath, err)
	}
	if err := v.Set(key, []byte(value)); err != nil {
		t.Fatalf("set: %v", err)
	}
}

func master(t *testing.T) *secret.Master {
	t.Helper()
	m, err := secret.DeriveMaster("passphrase", []byte("salt-16-bytes-!!"), fast)
	if err != nil {
		t.Fatalf("DeriveMaster: %v", err)
	}
	return m
}

// A shard key is deterministic, 32 bytes, and bound to (workspace, relPath): a
// different placement yields a different key.
func TestShardKey_BoundToPlacement(t *testing.T) {
	m := master(t)

	k := m.ShardKey("ws", "plugins/gmail")
	if len(k) != 32 {
		t.Fatalf("ShardKey len = %d, want 32", len(k))
	}
	if !bytes.Equal(k, m.ShardKey("ws", "plugins/gmail")) {
		t.Error("ShardKey must be deterministic")
	}
	if bytes.Equal(k, m.ShardKey("ws", "plugins/evil")) {
		t.Error("a different folder must derive a different key")
	}
	if bytes.Equal(k, m.ShardKey("other-ws", "plugins/gmail")) {
		t.Error("a different workspace must derive a different key")
	}
	// The workspace vault key and a shard key never collide.
	if bytes.Equal(m.WorkspaceKey("ws"), k) {
		t.Error("workspace key and shard key must differ")
	}
}

// The NUL-delimited, versioned info makes it impossible for two distinct
// (wsName, relPath) pairs to collide into one key — the classic delimiter attack
// (a ":"-join of free-form strings) does not apply.
func TestShardKey_NoDelimiterCollision(t *testing.T) {
	m := master(t)
	// With a naive "ws:relPath" join these two would collide; with NUL framing they do not.
	if bytes.Equal(m.ShardKey("a", "b/c"), m.ShardKey("a/b", "c")) {
		t.Error("distinct (ws, relPath) pairs must never derive the same key")
	}
}

// A shard is a Vault at a path with a path-derived key and AAD=relPath. It opens with
// the right key, and fails closed with the wrong path's key OR the wrong AAD — so a
// shard copied into another folder cannot be decrypted.
func TestShardVault_FailsClosedCrossFolder(t *testing.T) {
	m := master(t)
	dir := t.TempDir()
	gmailPath := filepath.Join(dir, "gmail.enc")

	// Seal a secret into gmail's shard.
	gv, err := secret.OpenVault(gmailPath, m.ShardKey("ws", "plugins/gmail"), secret.WithAAD([]byte("plugins/gmail")))
	if err != nil {
		t.Fatalf("open gmail shard: %v", err)
	}
	if err := gv.Set("token", []byte("s3cr3t")); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Right key + right AAD → opens and reads.
	reopen, err := secret.OpenVault(gmailPath, m.ShardKey("ws", "plugins/gmail"), secret.WithAAD([]byte("plugins/gmail")))
	if err != nil {
		t.Fatalf("reopen with correct key/AAD: %v", err)
	}
	if v, ok := reopen.Get("token"); !ok || string(v) != "s3cr3t" {
		t.Fatalf("reopen value = %q, %v", v, ok)
	}

	// Wrong folder's key (as if an impostor in plugins/evil tried to open gmail's file).
	if _, err := secret.OpenVault(gmailPath, m.ShardKey("ws", "plugins/evil"), secret.WithAAD([]byte("plugins/gmail"))); !errors.Is(err, secret.ErrWrongPassphrase) {
		t.Fatalf("cross-folder key: got %v, want ErrWrongPassphrase", err)
	}
	// Right key but wrong AAD (as if the file were copied to another folder path).
	if _, err := secret.OpenVault(gmailPath, m.ShardKey("ws", "plugins/gmail"), secret.WithAAD([]byte("plugins/evil"))); !errors.Is(err, secret.ErrWrongPassphrase) {
		t.Fatalf("wrong AAD: got %v, want ErrWrongPassphrase", err)
	}
}

// LoadShardsInto folds each folder's secret into the resolution store; a shard that
// will not open is skipped fail-closed (no panic, no workspace-vault fallback), and
// the good shards still load.
func TestLoadShardsInto_LoadsAndFailsClosed(t *testing.T) {
	m := master(t)
	wsDir := t.TempDir()

	writeShard(t, m, wsDir, "ws", "plugins/gmail", "plugin:gmail/acct", "s3cr3t")
	writeShard(t, m, wsDir, "ws", "mcp/github", "mcp:github@api.github.com/oauth", "tok")

	// A corrupt/foreign shard: bytes that are not a valid vault → must be skipped, not fatal.
	badDir := filepath.Join(wsDir, "plugins", "broken")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "secrets.enc"), []byte("not a vault"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := secret.NewStore()
	secret.LoadShardsInto(res, m, wsDir, "ws", anyName, quiet())

	if !res.Exists("plugin:gmail/acct") {
		t.Error("gmail shard secret must be loaded into the resolution store")
	}
	if !res.Exists("mcp:github@api.github.com/oauth") {
		t.Error("mcp github shard secret must be loaded")
	}
	// The broken shard contributed nothing — and did not crash or abort the load.
}

// A shard is only decryptable from its OWN folder path: pointing LoadShardsInto at a
// workspace whose NAME differs derives different keys, so nothing loads (fail-closed).
func TestLoadShardsInto_WrongWorkspaceName_NothingLoads(t *testing.T) {
	m := master(t)
	wsDir := t.TempDir()
	writeShard(t, m, wsDir, "ws", "plugins/gmail", "plugin:gmail/acct", "s3cr3t")

	res := secret.NewStore()
	secret.LoadShardsInto(res, m, wsDir, "OTHER-WS", anyName, quiet()) // wrong ws name → wrong key

	if res.Exists("plugin:gmail/acct") {
		t.Error("a shard sealed for workspace 'ws' must not decrypt under another workspace's key")
	}
}
