package secret

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"filippo.io/age"
)

// This file is the vault: the encrypted persistence of the Store. The design is
// SOPS-style — the workspace carries ONLY ciphertext (secrets.age), so it stays
// fully portable and even committable; the passphrase lives in the user's head
// and is never written anywhere. Encryption is age with a scrypt (passphrase)
// recipient — audited, pure-Go crypto, never hand-rolled here.
//
// The Vault is host-only, like the Injector: it is never handed to a guest. The
// guest boundary stays GuestView over the Store (presence only). Get is
// exported on the Vault — not the Store — precisely so the "no exported Store
// method returns a value" invariant keeps holding for anything a guest could
// ever be handed.

// ErrWrongPassphrase is returned by OpenVault when the vault file exists but
// the passphrase does not decrypt it — fail-closed, so a typo can never
// silently yield an empty vault (and a later Set can never clobber the real one).
var ErrWrongPassphrase = errors.New("wrong vault passphrase")

// vaultVersion is the serialization version inside the encrypted blob; an
// unknown version is rejected fail-closed rather than half-parsed.
const vaultVersion = 1

// maxVaultBytes bounds both the ciphertext file and the decrypted plaintext —
// a bomb/corruption sanity cap, far above any legitimate secret set.
const maxVaultBytes = 16 << 20 // 16 MiB

// defaultWorkFactor is age's own scrypt default (2^18); explicit so the cost
// of a passphrase guess is a stated choice, not an upstream accident.
const defaultWorkFactor = 18

// vaultFile is the plaintext serialization inside the age envelope. []byte
// values marshal as base64 — the store stays kind-agnostic bytes.
type vaultFile struct {
	Version int               `json:"version"`
	Secrets map[string][]byte `json:"secrets"`
}

// Vault is the Store's encrypted persistence: it owns a path, a passphrase,
// and the Store it loaded. Every mutation goes through the Vault (Set), which
// re-encrypts and re-persists atomically — the plaintext never touches disk.
// Concurrency-safe (a token refresh may Set from another goroutine).
type Vault struct {
	path       string
	passphrase string
	workFactor int

	mu    sync.Mutex // serializes mutate+persist
	store *Store
}

// VaultOption configures OpenVault.
type VaultOption func(*Vault)

// WithWorkFactor overrides the scrypt work factor (cost 2^logN) used when
// sealing. Meant for tests, where the default's key-derivation latency on
// every persist would dominate; production uses the default. Decryption always
// honors the factor recorded in the file header.
func WithWorkFactor(logN int) VaultOption {
	return func(v *Vault) { v.workFactor = logN }
}

// OpenVault loads the encrypted vault at path with passphrase. A missing file
// is a fresh, empty vault — persisted immediately, so the chosen passphrase
// sticks and the next start verifies against it. An existing file that the
// passphrase does not open is ErrWrongPassphrase; a corrupt, oversized, or
// unknown-version file is an error — all fail-closed, never a silent empty
// vault over a real one. An empty passphrase is rejected outright.
func OpenVault(path, passphrase string, opts ...VaultOption) (*Vault, error) {
	if passphrase == "" {
		return nil, errors.New("vault: empty passphrase")
	}
	v := &Vault{path: path, passphrase: passphrase, workFactor: defaultWorkFactor, store: NewStore()}
	for _, o := range opts {
		o(v)
	}

	ciphertext, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := v.persist(); err != nil { // fresh vault: seal it now so the passphrase sticks
			return nil, fmt.Errorf("vault: create %s: %w", path, err)
		}
		return v, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vault: read %s: %w", path, err)
	}
	if len(ciphertext) > maxVaultBytes {
		return nil, fmt.Errorf("vault: %s exceeds %d bytes", path, maxVaultBytes)
	}

	plaintext, err := openSealed(ciphertext, passphrase)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(plaintext))
	dec.DisallowUnknownFields()
	var f vaultFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("vault: parse %s: %w", path, err)
	}
	if f.Version != vaultVersion {
		return nil, fmt.Errorf("vault: %s has unsupported version %d (want %d)", path, f.Version, vaultVersion)
	}
	for name, value := range f.Secrets {
		v.store.Set(name, value)
	}
	return v, nil
}

// Store returns the in-memory store the vault loaded — the surface the
// injector and scanner (and, as GuestView, a guest) work against.
func (v *Vault) Store() *Store { return v.store }

// Set stores (or replaces) a secret and re-persists the encrypted vault. An
// unchanged value is a no-op (no pointless re-encryption on every startup).
// If persisting fails the in-memory store is NOT updated — disk and memory
// never diverge, so a "saved" credential is always actually saved.
func (v *Vault) Set(name string, value []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if old, ok := v.store.value(name); ok && bytes.Equal(old, value) {
		return nil
	}
	snap := v.store.snapshot()
	snap[name] = value
	if err := v.persistSnapshot(snap); err != nil {
		return err
	}
	v.store.Set(name, value)
	return nil
}

// Get is the host-side read of a secret value — used by the composition root,
// e.g. to seed an OAuth credential from its stored token. The Vault is never
// handed to a guest (the guest surface is GuestView), so this does not weaken
// the "guest sees presence only" boundary.
func (v *Vault) Get(name string) ([]byte, bool) {
	return v.store.value(name)
}

// persist seals and writes the store's current content.
func (v *Vault) persist() error {
	return v.persistSnapshot(v.store.snapshot())
}

// persistSnapshot seals secrets and writes them atomically (tmp + rename), dir
// 0700 / file 0600. Only ciphertext ever reaches the filesystem.
func (v *Vault) persistSnapshot(secrets map[string][]byte) error {
	plaintext, err := json.Marshal(vaultFile{Version: vaultVersion, Secrets: secrets})
	if err != nil {
		return err
	}
	ciphertext, err := seal(plaintext, v.passphrase, v.workFactor)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return err
	}
	tmp := v.path + ".tmp"
	if err := os.WriteFile(tmp, ciphertext, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, v.path)
}

// seal encrypts plaintext to an age scrypt (passphrase) recipient.
func seal(plaintext []byte, passphrase string, workFactor int) ([]byte, error) {
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	r.SetWorkFactor(workFactor)
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	if err != nil {
		return nil, fmt.Errorf("vault: encrypt: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("vault: encrypt: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("vault: encrypt: %w", err)
	}
	return buf.Bytes(), nil
}

// openSealed decrypts an age scrypt blob, mapping a non-matching identity to
// ErrWrongPassphrase and capping the plaintext at maxVaultBytes.
func openSealed(ciphertext []byte, passphrase string) ([]byte, error) {
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
		return nil, fmt.Errorf("vault: decrypt: %w", err)
	}
	plaintext, err := io.ReadAll(io.LimitReader(r, maxVaultBytes+1))
	if err != nil {
		// A truncated/tampered payload also surfaces here (age authenticates as
		// it streams) — fail closed rather than accept a partial vault.
		return nil, fmt.Errorf("vault: decrypt: %w", err)
	}
	if len(plaintext) > maxVaultBytes {
		return nil, fmt.Errorf("vault: decrypted payload exceeds %d bytes", maxVaultBytes)
	}
	return plaintext, nil
}
