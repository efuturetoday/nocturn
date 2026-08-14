//go:build ignore

// Command sign produces the Ed25519 signature a catalog plugin needs, and can mint the keypair.
//
//	go run sign.go -keygen                  print a new keypair (public key goes into signingKeys)
//	go run sign.go gmail                    sign plugins/gmail, writing plugins/gmail/plugin.sig
//	go run sign.go                          sign every plugin whose signature is missing or stale
//
// The private key is read from NOCTURN_CATALOG_SIGNING_KEY (base64) or -key <file>. It never enters
// this repository, and CI never needs it: the signature is committed BESIDE the plugin, and
// generate.go only copies it into the catalog. That is what keeps `go generate` reproducible on a
// machine that cannot sign anything.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/internal/library"
	"github.com/efuturetoday/nocturn/internal/plugin"
)

const (
	keyEnv  = "NOCTURN_CATALOG_SIGNING_KEY"
	sigFile = "plugin.sig"
)

func main() {
	keygen := flag.Bool("keygen", false, "mint a keypair and print it")
	keyPath := flag.String("key", "", "file holding the base64 private key (default: $"+keyEnv+")")
	flag.Parse()

	if *keygen {
		if err := mint(); err != nil {
			fail(err)
		}
		return
	}
	key, err := privateKey(*keyPath)
	if err != nil {
		fail(err)
	}
	if err := signAll(key, flag.Args()); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sign:", err)
	os.Exit(1)
}

// mint prints a fresh keypair. The private half is printed once and never stored by this tool —
// putting it on disk here would be the tool deciding where a signing key lives.
func mint() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Printf("public  (into library.signingKeys): %s\n", base64.StdEncoding.EncodeToString(pub))
	fmt.Printf("private (keep it out of this repo): %s\n", base64.StdEncoding.EncodeToString(priv))
	return nil
}

func privateKey(path string) (ed25519.PrivateKey, error) {
	encoded := os.Getenv(keyEnv)
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		encoded = strings.TrimSpace(string(data))
	}
	if encoded == "" {
		return nil, errors.New("no signing key: set $" + keyEnv + " or pass -key <file> (mint one with -keygen)")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("private key is not base64: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key is %d bytes, want %d", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

// signAll signs the named plugins, or every one under plugins/ when none are named.
func signAll(key ed25519.PrivateKey, names []string) error {
	if len(names) == 0 {
		entries, err := os.ReadDir("plugins")
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				names = append(names, e.Name())
			}
		}
	}
	for _, name := range names {
		if err := sign(key, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// sign writes plugins/<name>/plugin.sig over the same statement the daemon verifies.
func sign(key ed25519.PrivateKey, name string) error {
	dir := filepath.Join("plugins", name)
	manifest, err := os.ReadFile(filepath.Join(dir, plugin.ManifestFile))
	if err != nil {
		return err
	}
	script, err := os.ReadFile(filepath.Join(dir, plugin.ScriptFile))
	if err != nil {
		return err
	}
	bundled, err := os.ReadFile(filepath.Join(dir, plugin.SkillFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	skillSHA := ""
	if len(bundled) > 0 {
		skillSHA = digest(bundled)
	}
	msg := library.SignedStatement(name, name, digest(manifest), digest(script), skillSHA)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, msg))
	if err := os.WriteFile(filepath.Join(dir, sigFile), []byte(sig+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("signed %s\n", name)
	return nil
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
