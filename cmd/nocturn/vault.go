// Startup unlock of the encrypted secret vault. A single MASTER passphrase (asked
// once per launch) derives a distinct per-workspace key (HKDF), so the same
// passphrase opens every workspace's <ws>/secrets.vault without sharing a key between
// them. Runs on the plain terminal BEFORE bubbletea takes over. The passphrase is
// read without echo and lives only in memory; the workspace on disk carries nothing
// but AES-256-GCM ciphertext (plus the non-secret master.json salt/verifier).
package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/efuturetoday/nocturn/internal/secret"
)

// passphraseAttempts bounds how often a wrong passphrase may be retried before
// startup fails closed.
const passphraseAttempts = 3

// unlockMaster loads (or, first ever, creates) the master key from its descriptor at
// saltPath. First run: the user sets a master passphrase (entered twice) that opens
// ALL workspaces; a verifier is stored so a later typo is caught up front. Existing:
// up to passphraseAttempts tries checked against the verifier — no vault needed to
// verify, so a fresh workspace can never be created under a typo'd key. Requires a
// terminal (there is no env/file fallback: the passphrase stays in the user's head).
func unlockMaster(saltPath string) (*secret.Master, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, errors.New("vault: cannot prompt for the passphrase (stdin is not a terminal)")
	}

	salt, logN, verifier, err := secret.ReadMasterSalt(saltPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Printf("No master key yet — choose a master passphrase (it opens every workspace).\n")
		pass, err := chooseNewPassphrase()
		if err != nil {
			return nil, err
		}
		salt, logN, err := secret.NewMasterSalt()
		if err != nil {
			return nil, err
		}
		m, err := secret.DeriveMaster(pass, salt, secret.MasterWorkFactor(logN))
		if err != nil {
			return nil, err
		}
		if err := secret.WriteMasterSalt(saltPath, salt, logN, m.Verifier()); err != nil {
			return nil, err
		}
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vault: read %s: %w", saltPath, err)
	}

	for attempt := 1; ; attempt++ {
		pass, err := readPassphrase("Master passphrase: ")
		if err != nil {
			return nil, err
		}
		m, err := secret.DeriveMaster(pass, salt, secret.MasterWorkFactor(logN))
		if err != nil {
			return nil, err
		}
		if m.CheckVerifier(verifier) {
			return m, nil
		}
		if attempt >= passphraseAttempts {
			return nil, errors.New("vault: wrong master passphrase")
		}
		fmt.Println("Wrong passphrase, try again.")
	}
}

// unlockVault opens the workspace vault at vaultPath using the master-derived key for
// wsName. If the new-format file is absent but a legacy age vault (agePath) exists, it
// migrates once: the OLD age passphrase is asked separately, the secrets are re-sealed
// under the new key, and the age file is kept as ".bak". A missing file (no legacy
// either) is a fresh, empty vault.
func unlockVault(m *secret.Master, vaultPath, wsName, agePath string) (*secret.Vault, error) {
	key := m.WorkspaceKey(wsName)

	if _, err := os.Stat(vaultPath); errors.Is(err, os.ErrNotExist) && secret.IsAgeVault(agePath) {
		fmt.Printf("Migrating legacy vault %s → %s (age → AES-256-GCM)…\n", agePath, vaultPath)
		oldPass, err := readPassphrase("OLD (age) vault passphrase: ")
		if err != nil {
			return nil, err
		}
		if err := secret.MigrateAgeVault(agePath, vaultPath, oldPass, key); err != nil {
			return nil, fmt.Errorf("vault: migrate %s: %w", agePath, err)
		}
		if err := os.Rename(agePath, agePath+".bak"); err != nil {
			return nil, fmt.Errorf("vault: back up %s: %w", agePath, err)
		}
		fmt.Printf("Migrated. Old file kept as %s.bak — delete it once satisfied.\n", agePath)
	}

	return secret.OpenVault(vaultPath, key)
}

// chooseNewPassphrase asks for a fresh passphrase twice (typos are invisible without
// echo) and rejects empty or mismatched entries with a retry.
func chooseNewPassphrase() (string, error) {
	for attempt := 1; ; attempt++ {
		pass, err := readPassphrase("Choose a master passphrase: ")
		if err != nil {
			return "", err
		}
		if pass == "" {
			fmt.Println("The passphrase must not be empty.")
		} else {
			confirm, err := readPassphrase("Confirm passphrase: ")
			if err != nil {
				return "", err
			}
			if pass == confirm {
				return pass, nil
			}
			fmt.Println("Passphrases do not match, try again.")
		}
		if attempt >= passphraseAttempts {
			return "", errors.New("vault: no passphrase set")
		}
	}
}

// readPassphrase prompts and reads one line without echo.
func readPassphrase(prompt string) (string, error) {
	fmt.Print(prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println() // ReadPassword swallows the newline the user typed
	if err != nil {
		return "", fmt.Errorf("vault: read passphrase: %w", err)
	}
	return string(b), nil
}
