// Startup unlock of the encrypted secret vault (<ws>/secrets.age). Runs on
// the plain terminal BEFORE bubbletea takes over — like the plugin/MCP review
// prompts. The passphrase is read without echo and lives only in memory; the
// workspace on disk carries nothing but age ciphertext.
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

// unlockVault opens the vault at path, prompting for its passphrase. First run
// (no file yet): the user sets a passphrase (entered twice); the fresh vault is
// sealed immediately so the choice sticks. Existing file: up to
// passphraseAttempts tries, then fail-closed — a wrong passphrase never yields
// an empty vault. Requires a terminal on stdin (there is no env/file fallback
// on purpose: the passphrase stays in the user's head).
func unlockVault(path string) (*secret.Vault, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, errors.New("vault: cannot prompt for the passphrase (stdin is not a terminal)")
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Printf("No secret vault yet — creating %s (age-encrypted; only ciphertext touches disk).\n", path)
		pass, err := chooseNewPassphrase()
		if err != nil {
			return nil, err
		}
		return secret.OpenVault(path, pass)
	}

	for attempt := 1; ; attempt++ {
		pass, err := readPassphrase("Vault passphrase: ")
		if err != nil {
			return nil, err
		}
		v, err := secret.OpenVault(path, pass)
		if errors.Is(err, secret.ErrWrongPassphrase) && attempt < passphraseAttempts {
			fmt.Println("Wrong passphrase, try again.")
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("unlock %s: %w", path, err)
		}
		return v, nil
	}
}

// chooseNewPassphrase asks for a fresh passphrase twice (typos are invisible
// without echo) and rejects empty or mismatched entries with a retry.
func chooseNewPassphrase() (string, error) {
	for attempt := 1; ; attempt++ {
		pass, err := readPassphrase("Choose a vault passphrase: ")
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
