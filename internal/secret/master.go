package secret

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/scrypt"
)

// This file is the key hierarchy: ONE human passphrase → one Master key → a distinct
// per-workspace vault key. It lets a single passphrase open every workspace's vault
// (and, later, a daemon unlock them all at once) WITHOUT sharing a key between them:
//
//	master = scrypt(passphrase, salt)                     // slow, memory-hard, once
//	key_ws = HKDF-SHA256(master, "nocturn:workspace:"+ws) // fast, domain-separated
//
// The scrypt step defends the low-entropy passphrase against brute force; HKDF then
// derives high-entropy per-workspace keys cheaply. Domain separation by name means a
// key leaked for one workspace reveals nothing about another. The at-rest key scheme
// is NOT the isolation boundary — that stays the per-workspace Injector/Guard/Cage;
// this only removes the "N passphrases" tax and enables one-unlock-for-all.

// masterWorkFactor is the scrypt cost (2^logN); scrypt's r=8, p=1. 2^18 matches age's
// long-standing default — a stated cost, not an accident.
const masterWorkFactor = 18

// Master is the root key every workspace vault key derives from. Host-only; never
// handed to a guest, never written to disk (only the non-secret salt is persisted).
type Master struct{ key []byte }

type masterCfg struct{ logN int }

// MasterOption configures DeriveMaster.
type MasterOption func(*masterCfg)

// MasterWorkFactor overrides the scrypt cost (2^logN). Meant for tests, where the
// default's latency would dominate; production uses the stored/default factor.
func MasterWorkFactor(logN int) MasterOption {
	return func(c *masterCfg) { c.logN = logN }
}

// DeriveMaster stretches a passphrase into a 32-byte master key via scrypt over salt.
// Empty passphrase or salt is rejected fail-closed. Slow by design — call once per
// process, then WorkspaceKey is cheap.
func DeriveMaster(passphrase string, salt []byte, opts ...MasterOption) (*Master, error) {
	if passphrase == "" {
		return nil, errors.New("secret: empty master passphrase")
	}
	if len(salt) == 0 {
		return nil, errors.New("secret: empty master salt")
	}
	cfg := masterCfg{logN: masterWorkFactor}
	for _, o := range opts {
		o(&cfg)
	}
	key, err := scrypt.Key([]byte(passphrase), salt, 1<<cfg.logN, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("secret: derive master: %w", err)
	}
	return &Master{key: key}, nil
}

// WorkspaceKey derives the 32-byte AES key for the named workspace's vault via HKDF,
// domain-separated by the workspace name. Deterministic (same master + name → same
// key), so it reproduces on every launch.
func (m *Master) WorkspaceKey(name string) []byte {
	return m.subKey("nocturn:workspace:" + name)
}

// subKey derives a 32-byte HKDF sub-key for info, domain-separated from every other.
func (m *Master) subKey(info string) []byte {
	// HKDF-Expand only errors on an absurd length (255*HashLen max); 32 never does.
	k, err := hkdf.Key(sha256.New, m.key, nil, info, 32)
	if err != nil {
		panic("secret: hkdf: " + err.Error())
	}
	return k
}

// masterVerifierPlaintext is the fixed token sealed under a dedicated verifier sub-key
// so the master passphrase can be checked WITHOUT any workspace vault — set once at
// first setup (double-confirmed), so a typo can never mint a broken fresh vault later.
const masterVerifierPlaintext = "nocturn-master-verifier-v1"

// Verifier returns a sealed blob to store in the master salt file. CheckVerifier(blob)
// later returns true only for the same passphrase (via the same derived master).
func (m *Master) Verifier() []byte {
	blob, err := seal([]byte(masterVerifierPlaintext), m.subKey("nocturn:master-verifier"))
	if err != nil {
		panic("secret: verifier seal: " + err.Error()) // AES-GCM over 26 bytes cannot fail
	}
	return blob
}

// CheckVerifier reports whether blob was produced by this master (i.e. the entered
// passphrase is correct). A mismatch (wrong passphrase) fails the GCM tag → false.
func (m *Master) CheckVerifier(blob []byte) bool {
	pt, err := openSealed(blob, m.subKey("nocturn:master-verifier"))
	return err == nil && string(pt) == masterVerifierPlaintext
}

// masterSaltFile is the on-disk master descriptor — NOT secret. It carries the scrypt
// salt (individualizes the KDF), the work factor (so a future cost bump never silently
// changes existing masters), and the verifier blob (passphrase check). Shared across
// all workspaces so one passphrase yields one master.
type masterSaltFile struct {
	Salt     []byte `json:"salt"`
	LogN     int    `json:"logN"`
	Verifier []byte `json:"verifier"`
}

// NewMasterSalt returns a fresh random 16-byte salt and the default work factor — for
// a first-ever master setup (before the passphrase is known).
func NewMasterSalt() (salt []byte, logN int, err error) {
	s := make([]byte, 16)
	if _, err := rand.Read(s); err != nil {
		return nil, 0, err
	}
	return s, masterWorkFactor, nil
}

// ReadMasterSalt reads the master descriptor at path. A missing file returns
// fs.ErrNotExist (the caller then does first-time setup).
func ReadMasterSalt(path string) (salt []byte, logN int, verifier []byte, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, nil, err // includes fs.ErrNotExist, handled by the caller
	}
	var ms masterSaltFile
	if err := json.Unmarshal(data, &ms); err != nil {
		return nil, 0, nil, fmt.Errorf("secret: parse master salt: %w", err)
	}
	if len(ms.Salt) == 0 || ms.LogN <= 0 || len(ms.Verifier) == 0 {
		return nil, 0, nil, errors.New("secret: invalid master salt file")
	}
	return ms.Salt, ms.LogN, ms.Verifier, nil
}

// WriteMasterSalt persists the master descriptor (0600). Called once at first setup.
func WriteMasterSalt(path string, salt []byte, logN int, verifier []byte) error {
	b, err := json.Marshal(masterSaltFile{Salt: salt, LogN: logN, Verifier: verifier})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
