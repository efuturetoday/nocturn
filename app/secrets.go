package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/app/secret"
)

const (
	dataDir   = "./nocturn-data"
	vaultFile = "vault.enc"
	saltFile  = "master.salt"
	bindFile  = "bindings.json"
)

// buildSecrets opens (or, on first run, initializes) the encrypted vault with the master passphrase
// and returns a credential injector the network tool injects from. It returns nil — injection simply
// off — when no passphrase is set, it is wrong, or the vault can't open; the assistant still runs,
// just without host-owned credentials. Secret VALUES are seeded from NOCTURN_SECRET_<NAME> env (the
// input channel until an interactive add-secret UX lands); host bindings come from bindings.json.
//
// This is the one unlock entrypoint; an interactive prompt (WebSocket/TUI) is a later slice that
// feeds the same passphrase here instead of the env. Both returns are nil when the vault stays
// locked — the assistant runs without credentials and without scanning.
func buildSecrets(log *slog.Logger) (*secret.Injector, *secret.Scanner) {
	vault, err := openVault()
	if err != nil {
		log.Warn("secret: vault stays locked", "err", err)
		return nil, nil
	}
	if vault == nil {
		return nil, nil // no passphrase — vault locked, no injection, no scanning
	}
	seedEnvSecrets(vault, log)
	injector := secret.NewInjector(vault.Store())
	loadBindings(injector, filepath.Join(dataDir, bindFile), log)
	registerOAuth(injector, vault, wsRoot, log) // refreshing token sources for authed OAuth plugins
	// The scanner screens traffic for the vault's known values (plus embedded gitleaks patterns), so
	// it shares the vault's store and lifecycle.
	scanner := secret.NewScanner(vault.Store())
	log.Info("secret: vault unlocked")
	return injector, scanner
}

// openVault unlocks (or first-run initializes) the encrypted vault with the master passphrase. It
// returns (nil, nil) when no passphrase is set — the vault simply stays locked. It is the shared
// entrypoint for both the daemon (buildSecrets) and `nocturn auth` (runAuth).
func openVault() (*secret.Vault, error) {
	pass := os.Getenv("NOCTURN_MASTER_PASSPHRASE")
	if pass == "" {
		return nil, nil
	}
	master, err := unlockMaster(pass, filepath.Join(dataDir, saltFile))
	if err != nil {
		return nil, err
	}
	return secret.OpenVault(filepath.Join(dataDir, vaultFile), master.WorkspaceKey("vault"))
}

// unlockMaster derives the master key from the passphrase, minting the salt on first run and
// verifying against the stored verifier afterwards (a wrong passphrase fails closed).
func unlockMaster(pass, saltPath string) (*secret.Master, error) {
	salt, logN, verifier, err := secret.ReadMasterSalt(saltPath)
	if err != nil {
		// First run: mint a salt and record a verifier so later unlocks can be checked.
		salt, logN, err = secret.NewMasterSalt()
		if err != nil {
			return nil, err
		}
		m, err := secret.DeriveMaster(pass, salt, secret.WithWorkFactor(logN))
		if err != nil {
			return nil, err
		}
		if err := secret.WriteMasterSalt(saltPath, salt, logN, m.Verifier()); err != nil {
			return nil, err
		}
		return m, nil
	}
	m, err := secret.DeriveMaster(pass, salt, secret.WithWorkFactor(logN))
	if err != nil {
		return nil, err
	}
	if !m.CheckVerifier(verifier) {
		return nil, errors.New("wrong master passphrase")
	}
	return m, nil
}

// seedEnvSecrets stores each NOCTURN_SECRET_<NAME>=value into the vault under <name> (lowercased) —
// the input channel for credential values until an interactive add-secret UX exists.
func seedEnvSecrets(vault *secret.Vault, log *slog.Logger) {
	const prefix = "NOCTURN_SECRET_"
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, prefix) || v == "" {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(k, prefix))
		if err := vault.Set(name, []byte(v)); err != nil {
			log.Warn("secret: seed", "name", name, "err", err)
		}
	}
}

// loadBindings reads bindings.json (a list of host-owned credential bindings) and registers each at
// the workspace level (owner ""), so the model's own network calls inject them. Absent file = none.
func loadBindings(inj *secret.Injector, path string, log *slog.Logger) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // no bindings configured
	}
	var raw []struct {
		Secret string `json:"secret"`
		Host   string `json:"host"`
		Header string `json:"header"`
		Prefix string `json:"prefix"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Warn("secret: bindings.json", "err", err)
		return
	}
	for _, b := range raw {
		inj.AddBinding("", secret.Binding{
			Secret: b.Secret,
			Host:   b.Host,
			Header: b.Header,
			Prefix: b.Prefix,
		})
	}
	log.Info("secret: bindings loaded", "count", len(raw))
}
