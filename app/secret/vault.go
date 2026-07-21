package secret

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// This file is the vault: the encrypted persistence of the Store. The design is
// SOPS-style — the workspace carries ONLY ciphertext (secrets.vault), so it stays
// fully portable and even committable; the key lives in the user's head (a master
// passphrase, see master.go) and is never written anywhere. Encryption is
// AES-256-GCM under a per-workspace key derived from the master (HKDF), with a fresh
// random nonce per seal and a fixed AAD binding the format version.
//
// The Vault is host-only, like the Injector: it is never handed to a guest. The
// guest boundary stays GuestView over the Store (presence only). Get is exported on
// the Vault — not the Store — precisely so the "no exported Store method returns a
// value" invariant keeps holding for anything a guest could ever be handed.

// ErrWrongPassphrase is returned by OpenVault when the vault file exists but the key
// does not authenticate it (GCM tag mismatch) — fail-closed, so a wrong key or a
// tampered file can never silently yield an empty vault (and a later Set can never
// clobber the real one).
var ErrWrongPassphrase = errors.New("wrong vault passphrase")

// vaultVersion is the serialization version of the plaintext inside the envelope; an
// unknown version is rejected fail-closed rather than half-parsed.
const vaultVersion = 1

// maxVaultBytes bounds both the ciphertext file and the decrypted plaintext — a
// bomb/corruption sanity cap, far above any legitimate secret set.
const maxVaultBytes = 16 << 20 // 16 MiB

// On-disk envelope: magic | format(1B) | nonce(12B) | AES-256-GCM(ciphertext+tag).
// The magic makes format detection cheap (and distinguishes an old age vault, which
// starts with "age-encryption.org"); the AAD authenticates the format so a
// cross-version confusion can't slip a differently-framed blob past the tag.
var (
	vaultMagic = []byte("NOCTURNV")
	vaultAAD   = []byte("nocturn-vault-v1")
)

const vaultFormat = 1

// vaultFile is the plaintext serialization inside the envelope. []byte values
// marshal as base64 — the store stays kind-agnostic bytes.
type vaultFile struct {
	Version int               `json:"version"`
	Secrets map[string][]byte `json:"secrets"`
}

// Vault is the Store's encrypted persistence: it owns a path, a 32-byte AES key, and
// the Store it loaded. Every mutation goes through the Vault (Set), which re-encrypts
// and re-persists atomically — the plaintext never touches disk. Concurrency-safe (a
// token refresh may Set from another goroutine).
type Vault struct {
	path string
	key  []byte // 32-byte AES-256 key (the workspace key derived from the master)

	mu    sync.Mutex // serializes mutate+persist
	store *Store
}

// OpenVault loads the encrypted vault at path with a 32-byte key. A missing file is a
// fresh, empty vault — persisted immediately, so the key sticks and the next start
// authenticates against it. An existing file the key does not authenticate is
// ErrWrongPassphrase; a corrupt, oversized, or unknown-version file is an error — all
// fail-closed, never a silent empty vault over a real one.
func OpenVault(path string, key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("vault: key must be 32 bytes, got %d", len(key))
	}
	v := &Vault{path: path, key: key, store: NewStore()}

	ciphertext, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := v.persist(); err != nil { // fresh vault: seal it now so the key sticks
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

	plaintext, err := openSealed(ciphertext, key)
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

// Store returns the in-memory store the vault loaded — the surface the injector and
// scanner (and, as GuestView, a guest) work against.
func (v *Vault) Store() *Store { return v.store }

// Set stores (or replaces) a secret and re-persists the encrypted vault. An unchanged
// value is a no-op (no pointless re-encryption on every startup). If persisting fails
// the in-memory store is NOT updated — disk and memory never diverge, so a "saved"
// credential is always actually saved.
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

// Get is the host-side read of a secret value — used by the composition root, e.g. to
// seed an OAuth credential from its stored token. The Vault is never handed to a guest
// (the guest surface is GuestView), so this does not weaken the "guest sees presence
// only" boundary.
func (v *Vault) Get(name string) ([]byte, bool) {
	return v.store.value(name)
}

// persist seals and writes the store's current content.
func (v *Vault) persist() error {
	return v.persistSnapshot(v.store.snapshot())
}

// persistSnapshot seals secrets and writes them atomically (tmp + rename), dir 0700 /
// file 0600. Only ciphertext ever reaches the filesystem.
func (v *Vault) persistSnapshot(secrets map[string][]byte) error {
	plaintext, err := json.Marshal(vaultFile{Version: vaultVersion, Secrets: secrets})
	if err != nil {
		return err
	}
	ciphertext, err := seal(plaintext, v.key)
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

// seal encrypts plaintext with AES-256-GCM under key: magic | format | nonce | ct+tag.
func seal(plaintext, key []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("vault: nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plaintext, vaultAAD)
	out := make([]byte, 0, len(vaultMagic)+1+len(nonce)+len(ct))
	out = append(out, vaultMagic...)
	out = append(out, vaultFormat)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// openSealed authenticates and decrypts a sealed blob. A GCM tag mismatch (wrong key
// or tampered ciphertext) maps to ErrWrongPassphrase; a bad frame is a plain error —
// all fail-closed.
func openSealed(ciphertext, key []byte) ([]byte, error) {
	if len(ciphertext) < len(vaultMagic)+1 || !bytes.Equal(ciphertext[:len(vaultMagic)], vaultMagic) {
		return nil, errors.New("vault: not a nocturn vault file")
	}
	if ciphertext[len(vaultMagic)] != vaultFormat {
		return nil, fmt.Errorf("vault: unsupported vault format %d", ciphertext[len(vaultMagic)])
	}
	body := ciphertext[len(vaultMagic)+1:]
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(body) < ns {
		return nil, errors.New("vault: truncated vault file")
	}
	nonce, ct := body[:ns], body[ns:]
	plaintext, err := gcm.Open(nil, nonce, ct, vaultAAD)
	if err != nil {
		return nil, ErrWrongPassphrase // auth fail = wrong key or tamper, fail closed
	}
	if len(plaintext) > maxVaultBytes {
		return nil, fmt.Errorf("vault: decrypted payload exceeds %d bytes", maxVaultBytes)
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	return gcm, nil
}
