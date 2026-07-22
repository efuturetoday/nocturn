package workspace

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/app/secret"
)

// testMaster derives a master with a tiny work factor so the scrypt cost doesn't dominate the test.
func testMaster(t *testing.T) *secret.Master {
	t.Helper()
	m, err := secret.DeriveMaster("passphrase", []byte("salt-salt-salt-1"), secret.WithWorkFactor(1))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestBuildWorkspaceSecrets_Isolation is the per-workspace guarantee: each workspace opens its own
// vault at <dir>/vault.enc under its own derived key, so a secret in one is neither present in nor
// decryptable by another. A locked master yields no injector/scanner/vault.
func TestBuildWorkspaceSecrets_Isolation(t *testing.T) {
	m := testMaster(t)
	log := slog.New(slog.DiscardHandler)

	dirA, dirB := t.TempDir(), t.TempDir()
	_, _, vaultA, err := buildWorkspaceSecrets(m, dirA, "a", log)
	if err != nil || vaultA == nil {
		t.Fatalf("workspace a: vault=%v err=%v", vaultA, err)
	}
	if _, err := os.Stat(filepath.Join(dirA, "vault.enc")); err != nil {
		t.Fatalf("vault not created at <dir>/vault.enc: %v", err)
	}
	if err := vaultA.Set("mcp:x@h/oauth", []byte("tokenA")); err != nil {
		t.Fatal(err)
	}

	_, _, vaultB, err := buildWorkspaceSecrets(m, dirB, "b", log)
	if err != nil || vaultB == nil {
		t.Fatalf("workspace b: vault=%v err=%v", vaultB, err)
	}
	if _, ok := vaultB.Get("mcp:x@h/oauth"); ok {
		t.Fatal("workspace b's vault sees workspace a's secret — not isolated")
	}

	// Crypto isolation: workspace b's key must NOT decrypt workspace a's vault file.
	if _, err := secret.OpenVault(filepath.Join(dirA, "vault.enc"), m.WorkspaceKey("b")); err == nil {
		t.Fatal("workspace b's key decrypted workspace a's vault — keys not domain-separated")
	}

	// A locked master (nil) runs the workspace without credentials.
	inj, sc, v, err := buildWorkspaceSecrets(nil, t.TempDir(), "c", log)
	if err != nil || inj != nil || sc != nil || v != nil {
		t.Fatalf("nil master must yield (nil,nil,nil,nil); got inj=%v sc=%v v=%v err=%v", inj, sc, v, err)
	}
}
