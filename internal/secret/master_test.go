package secret_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/secret"
)

const testLogN = 10 // cheap scrypt for tests; production uses 2^18

func TestDeriveMaster_RejectsEmpty(t *testing.T) {
	if _, err := secret.DeriveMaster("", []byte("salt"), secret.WithWorkFactor(testLogN)); err == nil {
		t.Error("empty passphrase must be rejected")
	}
	if _, err := secret.DeriveMaster("pw", nil, secret.WithWorkFactor(testLogN)); err == nil {
		t.Error("empty salt must be rejected")
	}
}

// A workspace key is deterministic (same master + name), 32 bytes, and DOMAIN-
// SEPARATED: two workspaces get different keys, and a different passphrase gives a
// different master (hence different keys) — a key leaked for one reveals nothing
// about another.
func TestMaster_WorkspaceKeyDomainSeparated(t *testing.T) {
	salt := []byte("a-16-byte-salt!!")
	m, err := secret.DeriveMaster("correct horse", salt, secret.WithWorkFactor(testLogN))
	if err != nil {
		t.Fatal(err)
	}
	a1, a2 := m.WorkspaceKey("default"), m.WorkspaceKey("default")
	b := m.WorkspaceKey("work")
	if len(a1) != 32 {
		t.Fatalf("key length = %d, want 32", len(a1))
	}
	if !bytes.Equal(a1, a2) {
		t.Error("WorkspaceKey not deterministic for the same name")
	}
	if bytes.Equal(a1, b) {
		t.Error("different workspaces must get different keys (domain separation)")
	}

	// A different passphrase → different master → different key for the same name.
	m2, err := secret.DeriveMaster("wrong horse", salt, secret.WithWorkFactor(testLogN))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a1, m2.WorkspaceKey("default")) {
		t.Error("a different passphrase must yield a different workspace key")
	}
}

// The master descriptor round-trips: NewMasterSalt → WriteMasterSalt (0600) →
// ReadMasterSalt returns the same salt/logN/verifier; a missing file is fs.ErrNotExist.
func TestMasterSalt_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.json")
	if _, _, _, err := secret.ReadMasterSalt(path); !os.IsNotExist(err) {
		t.Fatalf("missing file err = %v, want fs.ErrNotExist", err)
	}

	salt, logN, err := secret.NewMasterSalt()
	if err != nil {
		t.Fatal(err)
	}
	m, _ := secret.DeriveMaster("pw", salt, secret.WithWorkFactor(logN))
	if err := secret.WriteMasterSalt(path, salt, logN, m.Verifier()); err != nil {
		t.Fatal(err)
	}
	gotSalt, gotN, gotVer, err := secret.ReadMasterSalt(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSalt, salt) || gotN != logN || len(gotVer) == 0 {
		t.Fatal("master descriptor did not round-trip")
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("master file perms = %v err=%v, want 0600", fi.Mode().Perm(), err)
	}
}

// The verifier authenticates the passphrase WITHOUT any vault: the right passphrase's
// master passes, a wrong one fails (GCM tag) — so a typo is caught up front.
func TestMaster_Verifier(t *testing.T) {
	salt := []byte("verifier-16-byte")
	right, _ := secret.DeriveMaster("correct", salt, secret.WithWorkFactor(testLogN))
	blob := right.Verifier()
	if !right.CheckVerifier(blob) {
		t.Fatal("the correct master must pass its own verifier")
	}
	wrong, _ := secret.DeriveMaster("typo", salt, secret.WithWorkFactor(testLogN))
	if wrong.CheckVerifier(blob) {
		t.Fatal("a wrong passphrase's master must fail the verifier")
	}
}

// End to end: a master-derived workspace key opens a vault sealed with that same key
// — the whole passphrase → master → key_ws → AES-GCM vault chain round-trips.
func TestMaster_DerivedKeyOpensVault(t *testing.T) {
	salt := []byte("another-16-byte!")
	m, err := secret.DeriveMaster("pw", salt, secret.WithWorkFactor(testLogN))
	if err != nil {
		t.Fatal(err)
	}
	key := m.WorkspaceKey("default")
	path := filepath.Join(t.TempDir(), "secrets.vault")

	v, err := secret.OpenVault(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set("tok", []byte("s3cr3t")); err != nil {
		t.Fatal(err)
	}

	// Re-derive the key from the same passphrase+salt and reopen.
	m2, _ := secret.DeriveMaster("pw", salt, secret.WithWorkFactor(testLogN))
	re, err := secret.OpenVault(path, m2.WorkspaceKey("default"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := re.Get("tok"); !ok || string(got) != "s3cr3t" {
		t.Fatalf("reopened via re-derived key: %q %v", got, ok)
	}
}
