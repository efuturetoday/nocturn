package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/efuturetoday/nocturn/app/secret"
)

const (
	dataDir  = "./nocturn-data"
	saltFile = "master.salt"
)

// buildMaster unlocks (or, on first run, initializes) the single master key from the passphrase. It
// is the ROOT of every workspace's vault key — each workspace derives its own via
// master.WorkspaceKey(name) and opens its own vault, so secrets are isolated per workspace while one
// passphrase unlocks them all. Returns nil — vaults simply locked — when no passphrase is set, it is
// wrong, or the master can't derive; the assistant still runs, just without host-owned credentials.
func buildMaster(log *slog.Logger) *secret.Master {
	master, err := openMaster()
	if err != nil {
		log.With("component", "secret").Warn("secret: master locked", "err", err)
		return nil
	}
	return master // nil when no passphrase — vaults stay locked
}

// openMaster derives the master key from NOCTURN_MASTER_PASSPHRASE + dataDir/master.salt. It returns
// (nil, nil) when no passphrase is set. It is the shared entrypoint for both the daemon (buildMaster)
// and `nocturn auth` (runAuth), which then opens a specific workspace's vault under a derived key.
func openMaster() (*secret.Master, error) {
	pass := os.Getenv("NOCTURN_MASTER_PASSPHRASE")
	if pass == "" {
		return nil, nil
	}
	return unlockMaster(pass, filepath.Join(dataDir, saltFile))
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
