package secret_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/app/secret"
)

func key32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func TestOpenVault_KeyWrongLength_Rejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := secret.OpenVault(path, make([]byte, n)); err == nil {
			t.Errorf("OpenVault accepted a %d-byte key, want rejection", n)
		}
	}
}

func TestOpenVault_WrongKey_ErrWrongPassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v, err := secret.OpenVault(path, key32(0xA1))
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	if err := v.Set("api", []byte("token")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	_, err = secret.OpenVault(path, key32(0xB2))
	if !errors.Is(err, secret.ErrWrongPassphrase) {
		t.Fatalf("wrong key: got %v, want ErrWrongPassphrase", err)
	}
}

func TestOpenVault_TamperedCiphertext_FailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v, err := secret.OpenVault(path, key32(0x11))
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	if err := v.Set("api", []byte("token")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	// Flip the last byte (the GCM tag) — authentication must fail closed.
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("rewrite vault: %v", err)
	}

	if _, err := secret.OpenVault(path, key32(0x11)); !errors.Is(err, secret.ErrWrongPassphrase) {
		t.Fatalf("tampered ciphertext: got %v, want ErrWrongPassphrase", err)
	}
}

func TestVault_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	const plain = "plaintext-secret-value"

	v, err := secret.OpenVault(path, key32(0x22))
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	if err := v.Set("api", []byte(plain)); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The plaintext must never appear in the on-disk bytes.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	if bytes.Contains(raw, []byte(plain)) {
		t.Fatal("plaintext secret found in the on-disk vault file")
	}

	// Reopening with the right key recovers the value.
	v2, err := secret.OpenVault(path, key32(0x22))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := v2.Get("api")
	if !ok || !bytes.Equal(got, []byte(plain)) {
		t.Fatalf("round-trip: got %q ok=%v, want %q", got, ok, plain)
	}
}

func TestVault_SetPersistFailure_MemoryNotUpdated(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "vaultdir")
	path := filepath.Join(dir, "secrets.vault")

	v, err := secret.OpenVault(path, key32(0x33)) // creates dir + fresh vault
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}

	// Make the directory read-only so the atomic write of the tmp file fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := v.Set("new", []byte("value")); err == nil {
		t.Fatal("Set succeeded despite an unwritable directory")
	}
	// Disk and memory must not diverge: a failed persist leaves memory untouched.
	if _, ok := v.Get("new"); ok {
		t.Fatal("in-memory store updated after a persist failure")
	}
}

func TestOpenVault_MissingFile_FreshEmptyPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v, err := secret.OpenVault(path, key32(0x44))
	if err != nil {
		t.Fatalf("OpenVault on missing file: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fresh vault not persisted: %v", err)
	}
	if _, ok := v.Get("anything"); ok {
		t.Fatal("fresh vault is not empty")
	}
	// Reopening the freshly-sealed file with the same key must authenticate.
	if _, err := secret.OpenVault(path, key32(0x44)); err != nil {
		t.Fatalf("reopen fresh vault: %v", err)
	}
}

func TestVault_SetUnchangedValue_NoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v, err := secret.OpenVault(path, key32(0x55))
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	if err := v.Set("api", []byte("token")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Re-setting the identical value must not re-seal (fresh nonce) or rewrite.
	if err := v.Set("api", []byte("token")); err != nil {
		t.Fatalf("Set again: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("unchanged Set re-wrote the vault (nonce changed) — not a no-op")
	}
}

func TestOpenVault_BadMagic_Rejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	// An age vault, or any non-nocturn blob, must be rejected — never a silent
	// empty vault.
	if err := os.WriteFile(path, []byte("age-encryption.org/v1\n...."), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := secret.OpenVault(path, key32(0x66))
	if err == nil {
		t.Fatal("bad-magic file accepted")
	}
	if errors.Is(err, secret.ErrWrongPassphrase) {
		t.Fatalf("bad magic mapped to ErrWrongPassphrase, want a framing error: %v", err)
	}
}

func TestOpenVault_TruncatedFrame_Rejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	// magic ("NOCTURNV") + format byte, but no nonce → truncated frame.
	blob := append([]byte("NOCTURNV"), 0x01)
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := secret.OpenVault(path, key32(0x77)); err == nil {
		t.Fatal("truncated frame accepted")
	}
}

func TestVault_Persist_AtomicAndPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	path := filepath.Join(dir, "secrets.vault")
	v, err := secret.OpenVault(path, key32(0x88))
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	if err := v.Set("api", []byte("token")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// No .tmp left behind (atomic rename).
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("a .tmp file survived: err=%v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat vault: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("vault file perm = %o, want 0600", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("vault dir perm = %o, want 0700", perm)
	}
}

func TestVault_ConcurrentSet_NoRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v, err := secret.OpenVault(path, key32(0x99))
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			if err := v.Set("k"+string(rune('a'+i)), []byte{byte(i)}); err != nil {
				t.Errorf("Set: %v", err)
			}
		}()
	}
	wg.Wait()
}
