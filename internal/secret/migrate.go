package secret

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
)

// This file is the one-time bridge from the OLD vault format (age scrypt-passphrase,
// one passphrase per vault) to the current AES-256-GCM master-derived format. The
// filippo.io/age dependency exists ONLY for this path and can be dropped once no age
// vaults remain in the wild.

// IsAgeVault reports whether the file at path is an old age vault (so the caller
// knows to migrate). An age file begins with the literal "age-encryption.org".
func IsAgeVault(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	want := []byte("age-encryption.org")
	hdr := make([]byte, len(want))
	n, _ := io.ReadFull(f, hdr)
	return n == len(hdr) && bytes.Equal(hdr, want)
}

// MigrateAgeVault reads the old age vault at agePath, decrypts it with agePassphrase,
// and re-seals its secrets as a new AES-256-GCM vault at newPath under key (the
// workspace key derived from the master). A wrong age passphrase is ErrWrongPassphrase
// (fail-closed); a non-vault payload is rejected before anything is written.
func MigrateAgeVault(agePath, newPath, agePassphrase string, key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("vault: key must be 32 bytes, got %d", len(key))
	}
	ciphertext, err := os.ReadFile(agePath)
	if err != nil {
		return fmt.Errorf("vault: read %s: %w", agePath, err)
	}
	if len(ciphertext) > maxVaultBytes {
		return fmt.Errorf("vault: %s exceeds %d bytes", agePath, maxVaultBytes)
	}
	plaintext, err := openAgeSealed(ciphertext, agePassphrase)
	if err != nil {
		return err
	}
	var f vaultFile
	if err := json.Unmarshal(plaintext, &f); err != nil {
		return fmt.Errorf("vault: parse legacy vault: %w", err)
	}
	if f.Version != vaultVersion {
		return fmt.Errorf("vault: legacy vault has unsupported version %d", f.Version)
	}
	v := &Vault{path: newPath, key: key, store: NewStore()}
	for name, value := range f.Secrets {
		v.store.Set(name, value)
	}
	return v.persist() // seals + writes atomically in the new format
}

// openAgeSealed decrypts an age scrypt blob (legacy format only).
func openAgeSealed(ciphertext []byte, passphrase string) ([]byte, error) {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), id)
	var noMatch *age.NoIdentityMatchError
	if errors.As(err, &noMatch) {
		return nil, ErrWrongPassphrase
	}
	if err != nil {
		return nil, fmt.Errorf("vault: decrypt legacy: %w", err)
	}
	plaintext, err := io.ReadAll(io.LimitReader(r, maxVaultBytes+1))
	if err != nil {
		return nil, fmt.Errorf("vault: decrypt legacy: %w", err)
	}
	return plaintext, nil
}
