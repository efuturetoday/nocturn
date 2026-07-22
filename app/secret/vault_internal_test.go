package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeal_UsesAAD_CrossVersionRejected proves the AAD is authenticated: a blob
// framed exactly like a nocturn vault, but whose GCM was computed under a
// DIFFERENT AAD, does not open — so a cross-version-confused blob can't slip
// past the tag. The control case (same AAD) opens.
func TestSeal_UsesAAD_CrossVersionRejected(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x5A
	}
	plaintext := []byte("payload")

	frame := func(aad []byte) []byte {
		gcm, err := newGCM(key)
		if err != nil {
			t.Fatalf("newGCM: %v", err)
		}
		nonce := make([]byte, gcm.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			t.Fatalf("nonce: %v", err)
		}
		ct := gcm.Seal(nil, nonce, plaintext, aad)
		out := append([]byte{}, vaultMagic...)
		out = append(out, vaultFormat)
		out = append(out, nonce...)
		return append(out, ct...)
	}

	// Control: sealed under the real AAD opens.
	if _, err := openSealed(frame(vaultAAD), key); err != nil {
		t.Fatalf("control (correct AAD) failed to open: %v", err)
	}
	// Cross-version: sealed under a different AAD must fail closed.
	if _, err := openSealed(frame([]byte("nocturn-vault-v2")), key); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong AAD: got %v, want ErrWrongPassphrase", err)
	}
}

// TestOpenVault_UnknownVersion_Rejected: a correctly sealed envelope whose inner
// plaintext declares an unsupported version is rejected fail-closed, not
// half-parsed.
func TestOpenVault_UnknownVersion_Rejected(t *testing.T) {
	key := make([]byte, 32)
	plaintext, err := json.Marshal(vaultFile{Version: 99, Secrets: map[string][]byte{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	blob, err := seal(plaintext, key)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "secrets.vault")
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = OpenVault(path, key)
	if err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("unknown version: got %v, want an 'unsupported version' error", err)
	}
}

// TestOpenVault_OversizeCiphertext_Rejected: a file larger than the sanity cap
// is rejected before any decrypt work.
func TestOpenVault_OversizeCiphertext_Rejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	big := make([]byte, maxVaultBytes+1)
	copy(big, vaultMagic)
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := OpenVault(path, make([]byte, 32))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize: got %v, want an 'exceeds' error", err)
	}
}

// TestSeal_NonceUniquePerSeal: each seal draws a fresh random nonce, so two
// seals of the same plaintext differ.
func TestSeal_NonceUniquePerSeal(t *testing.T) {
	key := make([]byte, 32)
	pt := []byte("same-plaintext")
	b1, err := seal(pt, key)
	if err != nil {
		t.Fatalf("seal 1: %v", err)
	}
	b2, err := seal(pt, key)
	if err != nil {
		t.Fatalf("seal 2: %v", err)
	}
	// nonce sits at magic(8) + format(1) .. +12.
	off := len(vaultMagic) + 1
	n1, n2 := b1[off:off+12], b2[off:off+12]
	if bytes.Equal(n1, n2) {
		t.Fatal("two seals reused the same nonce")
	}
	if bytes.Equal(b1, b2) {
		t.Fatal("two seals of the same plaintext produced identical ciphertext")
	}
}
