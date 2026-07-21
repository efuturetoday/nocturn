package secret_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/internal/secret"
)

// Fixed 32-byte AES keys for tests (the vault takes a key, not a passphrase — key
// derivation from a master is tested in master_test.go).
var (
	testKey  = bytes.Repeat([]byte{0xA5}, 32)
	otherKey = bytes.Repeat([]byte{0x5A}, 32)
)

func openTestVault(t *testing.T, path string) *secret.Vault {
	t.Helper()
	v, err := secret.OpenVault(path, testKey)
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	return v
}

// First run: a missing file is a fresh, empty vault — persisted immediately so the
// key sticks. A Set re-persists; a reopen with the same key sees the secret again.
// The full encrypt→decrypt roundtrip.
func TestVault_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")

	v := openTestVault(t, path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fresh vault must be persisted immediately: %v", err)
	}
	if err := v.Set("google", []byte(`{"refresh_token":"r-1"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}

	re := openTestVault(t, path)
	got, ok := re.Get("google")
	if !ok || string(got) != `{"refresh_token":"r-1"}` {
		t.Fatalf("reloaded secret = %q, %v", got, ok)
	}
	if !re.Store().Exists("google") {
		t.Fatal("reloaded store must report the secret present")
	}

	// A second Set on the reopened vault persists too (every change re-seals).
	if err := re.Set("api-key", []byte("k-2")); err != nil {
		t.Fatalf("Set 2: %v", err)
	}
	re2 := openTestVault(t, path)
	if got, ok := re2.Get("api-key"); !ok || string(got) != "k-2" {
		t.Fatalf("second secret not re-persisted: %q, %v", got, ok)
	}
	if got, ok := re2.Get("google"); !ok || string(got) != `{"refresh_token":"r-1"}` {
		t.Fatalf("first secret lost on re-persist: %q, %v", got, ok)
	}
}

// The wrong key fails closed with the sentinel error (GCM tag mismatch) — never a
// silent empty vault over the real one.
func TestVault_WrongKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v := openTestVault(t, path)
	if err := v.Set("k", []byte("v")); err != nil {
		t.Fatal(err)
	}

	if _, err := secret.OpenVault(path, otherKey); !errors.Is(err, secret.ErrWrongPassphrase) {
		t.Fatalf("err = %v, want ErrWrongPassphrase", err)
	}
}

// A key of the wrong length is rejected outright — a mis-derived key must never
// become an effectively unusable-yet-created vault.
func TestVault_BadKeyLength(t *testing.T) {
	if _, err := secret.OpenVault(filepath.Join(t.TempDir(), "secrets.vault"), []byte("too-short")); err == nil {
		t.Fatal("a non-32-byte key must be rejected")
	}
}

// The workspace carries ONLY ciphertext: the file is a binary blob (nocturn magic)
// whose bytes never contain the secret name or value in the clear.
func TestVault_FileIsCiphertextOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v := openTestVault(t, path)
	if err := v.Set("google", []byte("super-secret-refresh-token")); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("NOCTURNV")) {
		t.Fatalf("file does not start with the nocturn vault magic: %q", data[:min(len(data), 16)])
	}
	if bytes.Contains(data, []byte("super-secret-refresh-token")) || bytes.Contains(data, []byte("google")) {
		t.Fatal("plaintext secret material leaked into the vault file")
	}
}

// A corrupt / non-vault file is an error — fail-closed, not an empty vault (which a
// later Set would then overwrite, destroying the real one).
func TestVault_CorruptFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	if err := os.WriteFile(path, []byte("not a nocturn vault file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := secret.OpenVault(path, testKey); err == nil {
		t.Fatal("corrupt vault must not open")
	}
}

// A tampered ciphertext (flip a byte in the sealed body) fails the GCM tag — the
// vault refuses a modified file rather than returning partial/forged plaintext.
func TestVault_TamperFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v := openTestVault(t, path)
	if err := v.Set("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xFF // flip the last tag byte
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := secret.OpenVault(path, testKey); !errors.Is(err, secret.ErrWrongPassphrase) {
		t.Fatalf("tampered vault: err = %v, want ErrWrongPassphrase", err)
	}
}

// The vault-backed store is a regular Store: the injector stamps a vault secret at
// the border exactly as before (owner scoping, kind and host matching all
// unchanged — those semantics are covered in secret_test.go).
func TestVault_InjectorOverVaultStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v := openTestVault(t, path)
	if err := v.Set("mcp:github/oauth", []byte("ghp_pat123")); err != nil {
		t.Fatal(err)
	}

	in := secret.NewInjector(v.Store())
	in.AddBinding("mcp:github", secret.Binding{
		Secret: "mcp:github/oauth", Host: "api.github.com",
		Header: "Authorization", Prefix: "Bearer ",
	})

	req := &secret.Request{URL: "https://api.github.com/user"}
	if _, err := in.InjectMatching(secret.WithOwner(context.Background(), "mcp:github"), req, "api.github.com"); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if got := req.Headers["Authorization"]; got != "Bearer ghp_pat123" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Concurrent Sets (e.g. two token refreshes racing) are safe and all land in the
// persisted vault. Run with -race.
func TestVault_ConcurrentSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v := openTestVault(t, path)

	const n = 8
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := v.Set(fmt.Sprintf("k%d", i), []byte(strings.Repeat("v", i+1))); err != nil {
				t.Errorf("Set k%d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	re := openTestVault(t, path)
	for i := range n {
		if got, ok := re.Get(fmt.Sprintf("k%d", i)); !ok || len(got) != i+1 {
			t.Errorf("k%d = %q, %v after reload", i, got, ok)
		}
	}
}
