package secret_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/app/secret"
)

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
