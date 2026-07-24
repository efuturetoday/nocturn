package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/workspace"
)

func mustMaster(t *testing.T) *secret.Master {
	t.Helper()
	m, err := secret.DeriveMaster("pw", []byte("salt-16-bytes-!!"), secret.WithWorkFactor(10))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// ShardTokens routes a credential's SecretName to its owner's folder shard — a plugin token to
// plugins/<name>/secrets.enc, an mcp token (host-bound name) to mcp/<name>/secrets.enc — and never
// touches the workspace vault. A non-shard-owned name is rejected, and an unauthorized credential
// reads absent (fail-closed, no fallback).
func TestShardTokens_RoutesToFolderShard(t *testing.T) {
	m := mustMaster(t)
	wsDir := t.TempDir()
	tok := workspace.NewShardTokens(m, wsDir, "main", nil)

	// A plugin credential lands in its plugin folder's shard and reads back.
	if err := tok.Set("plugin:gmail/acct", []byte("PTOK")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "plugins", "gmail", "secrets.enc")); err != nil {
		t.Fatalf("plugin token must land in plugins/gmail/secrets.enc: %v", err)
	}
	if v, ok := tok.Get("plugin:gmail/acct"); !ok || string(v) != "PTOK" {
		t.Fatalf("get plugin = %q, %v", v, ok)
	}

	// An mcp credential (host-bound name) lands in its server folder's shard.
	if err := tok.Set("mcp:github@api.github.com/oauth", []byte("MTOK")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "mcp", "github", "secrets.enc")); err != nil {
		t.Fatalf("mcp token must land in mcp/github/secrets.enc: %v", err)
	}
	if v, ok := tok.Get("mcp:github@api.github.com/oauth"); !ok || string(v) != "MTOK" {
		t.Fatalf("get mcp = %q, %v", v, ok)
	}

	// The workspace vault is never written by the token store — compartmentalization holds on disk.
	if _, err := os.Stat(filepath.Join(wsDir, "vault.enc")); !os.IsNotExist(err) {
		t.Errorf("token store must not write vault.enc, stat err = %v", err)
	}

	// A name that is not an owner-namespaced credential is never routed.
	if _, ok := tok.Get("bare-secret"); ok {
		t.Error("a bare workspace secret is not shard-owned")
	}
	if err := tok.Set("bare-secret", nil); err == nil {
		t.Error("Set of a non-shard-owned name must error")
	}

	// An unauthorized credential (no shard yet) reads absent — never a fallback to another store.
	if _, ok := tok.Get("plugin:other/acct"); ok {
		t.Error("an unauthorized credential must read absent")
	}
}

// An OAuth provider record round-trips through the credential's shard beside its token,
// routed to the same folder, and reads back as absent when never stored.
func TestOAuthRecord_RoundTripInShard(t *testing.T) {
	m := mustMaster(t)
	wsDir := t.TempDir()
	tok := workspace.NewShardTokens(m, wsDir, "main", nil)

	const sn = "mcp:github@api.githubcopilot.com/oauth"
	if _, ok := workspace.LoadOAuthRecord(tok, sn); ok {
		t.Fatal("no record should exist yet")
	}
	rec := workspace.OAuthRecord{
		AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token",
		ClientID: "dyn-123", Resource: "https://api.githubcopilot.com/mcp", Scopes: []string{"repo"},
	}
	if err := workspace.StoreOAuthRecord(tok, sn, rec); err != nil {
		t.Fatalf("store record: %v", err)
	}
	// It lands in the server's own shard, not the workspace vault.
	if _, err := os.Stat(filepath.Join(wsDir, "mcp", "github", "secrets.enc")); err != nil {
		t.Fatalf("record must live in mcp/github/secrets.enc: %v", err)
	}
	got, ok := workspace.LoadOAuthRecord(tok, sn)
	if !ok || got.ClientID != "dyn-123" || got.Resource != "https://api.githubcopilot.com/mcp" || len(got.Scopes) != 1 {
		t.Fatalf("loaded record = %+v, ok=%v", got, ok)
	}
}
