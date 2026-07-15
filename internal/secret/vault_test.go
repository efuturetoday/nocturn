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

// testWorkFactor keeps the scrypt key derivation cheap in tests; production
// uses the (much costlier) default. Decryption reads the factor from the file.
const testWorkFactor = 10

func openTestVault(t *testing.T, path, passphrase string) *secret.Vault {
	t.Helper()
	v, err := secret.OpenVault(path, passphrase, secret.WithWorkFactor(testWorkFactor))
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	return v
}

// First run: a missing file is a fresh, empty vault — persisted immediately so
// the chosen passphrase sticks. A Set re-persists; a reopen with the same
// passphrase sees the secret again. The full encrypt→decrypt roundtrip.
func TestVault_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.age")

	v := openTestVault(t, path, "correct horse")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fresh vault must be persisted immediately: %v", err)
	}
	if err := v.Set("google", []byte(`{"refresh_token":"r-1"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}

	re := openTestVault(t, path, "correct horse")
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
	re2 := openTestVault(t, path, "correct horse")
	if got, ok := re2.Get("api-key"); !ok || string(got) != "k-2" {
		t.Fatalf("second secret not re-persisted: %q, %v", got, ok)
	}
	if got, ok := re2.Get("google"); !ok || string(got) != `{"refresh_token":"r-1"}` {
		t.Fatalf("first secret lost on re-persist: %q, %v", got, ok)
	}
}

// The wrong passphrase fails closed with the sentinel error — never a silent
// empty vault over the real one.
func TestVault_WrongPassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.age")
	v := openTestVault(t, path, "right")
	if err := v.Set("k", []byte("v")); err != nil {
		t.Fatal(err)
	}

	if _, err := secret.OpenVault(path, "wrong", secret.WithWorkFactor(testWorkFactor)); !errors.Is(err, secret.ErrWrongPassphrase) {
		t.Fatalf("err = %v, want ErrWrongPassphrase", err)
	}
}

// An empty passphrase is rejected outright — a forgotten prompt result must
// never become an effectively unencrypted vault.
func TestVault_EmptyPassphraseRejected(t *testing.T) {
	if _, err := secret.OpenVault(filepath.Join(t.TempDir(), "secrets.age"), ""); err == nil {
		t.Fatal("empty passphrase must be rejected")
	}
}

// The workspace carries ONLY ciphertext: the file is a binary age blob whose
// bytes never contain the secret name or value in the clear.
func TestVault_FileIsCiphertextOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.age")
	v := openTestVault(t, path, "pw")
	if err := v.Set("google", []byte("super-secret-refresh-token")); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("age-encryption.org/v1")) {
		t.Fatalf("file does not start with the age header: %q", data[:min(len(data), 32)])
	}
	if bytes.Contains(data, []byte("super-secret-refresh-token")) || bytes.Contains(data, []byte("google")) {
		t.Fatal("plaintext secret material leaked into the vault file")
	}
}

// A corrupt vault file is an error — fail-closed, not an empty vault (which a
// later Set would then overwrite, destroying the real one).
func TestVault_CorruptFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.age")
	if err := os.WriteFile(path, []byte("not an age file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := secret.OpenVault(path, "pw"); err == nil {
		t.Fatal("corrupt vault must not open")
	}
}

// The vault-backed store is a regular Store: the injector stamps a vault
// secret at the border exactly as before (owner scoping, capability and host
// matching all unchanged — those semantics are covered in secret_test.go).
func TestVault_InjectorOverVaultStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.age")
	v := openTestVault(t, path, "pw")
	if err := v.Set("mcp:github/oauth", []byte("ghp_pat123")); err != nil {
		t.Fatal(err)
	}

	in := secret.NewInjector(v.Store())
	in.AddBinding("mcp:github", secret.Binding{
		Secret: "mcp:github/oauth", Capability: "http.write", Host: "api.github.com",
		Header: "Authorization", Prefix: "Bearer ",
	})

	req := &secret.Request{URL: "https://api.github.com/user"}
	if _, err := in.InjectMatching(secret.WithOwner(context.Background(), "mcp:github"), req, "http.write", "api.github.com"); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if got := req.Headers["Authorization"]; got != "Bearer ghp_pat123" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Concurrent Sets (e.g. two token refreshes racing) are safe and all land in
// the persisted vault. Run with -race.
func TestVault_ConcurrentSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.age")
	v := openTestVault(t, path, "pw")

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := v.Set(fmt.Sprintf("k%d", i), []byte(strings.Repeat("v", i+1))); err != nil {
				t.Errorf("Set k%d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	re := openTestVault(t, path, "pw")
	for i := 0; i < n; i++ {
		if got, ok := re.Get(fmt.Sprintf("k%d", i)); !ok || len(got) != i+1 {
			t.Errorf("k%d = %q, %v after reload", i, got, ok)
		}
	}
}
