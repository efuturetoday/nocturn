//go:build ignore

// Command sign produces the Ed25519 signature a catalog entry needs, and can mint the keypair.
//
//	go run sign.go entryread.go -keygen     print a new keypair (public key goes into signingKeys)
//	go run sign.go entryread.go gmail       sign extensions/gmail, writing its extension.sig
//	go run sign.go entryread.go             sign every entry in the tree
//
// The private key is read from NOCTURN_CATALOG_SIGNING_KEY (base64) or -key <file>. It never enters
// this repository, and CI never needs it: the signature is committed BESIDE the entry, and
// generate.go only copies it into the catalog. That is what keeps `go generate` reproducible on a
// machine that cannot sign anything.
//
// It reads each entry with the SAME reader generate.go uses (entryread.go), because what is signed
// must be what is published — a second reader would be a second opinion about what the bytes are, and
// the signature would vouch for whichever one was wrong.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/internal/library"
)

const keyEnv = "NOCTURN_CATALOG_SIGNING_KEY"

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

// mint prints a fresh keypair. The private half is printed once and never written: where it belongs
// is a password manager, not a file in the repository that publishes what it signs.
func mint() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Printf("public  (into library.signingKeys): %s\n", base64.StdEncoding.EncodeToString(pub))
	fmt.Printf("private (keep it out of this repo): %s\n", base64.StdEncoding.EncodeToString(priv))
	return nil
}

// privateKey reads the signing key from a file or the environment.
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
		return nil, errors.New("no signing key: set " + keyEnv + " or pass -key <file>")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("the key is not base64: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("the key is %d bytes, want %d", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

// signAll signs the named entries, or every entry in the tree when none are named.
func signAll(key ed25519.PrivateKey, names []string) error {
	if len(names) == 0 {
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(e.Name(), "_") {
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

// sign writes extensions/<name>/extension.sig over the same statement the daemon verifies.
func sign(key ed25519.PrivateKey, name string) error {
	it, err := readItem(filepath.Join(src, name), name)
	if err != nil {
		return err
	}
	msg := library.SignedStatement(library.Signed{
		ID:         it.ID,
		SHA256:     it.SHA256,
		ListingSHA: library.ListingDigest(it.Title, it.Description, it.Homepage, it.Tags),
		Serial:     it.Serial,
	})
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, msg))
	if err := os.WriteFile(filepath.Join(src, name, sigFile), []byte(sig+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("signed %s (serial %d)\n", name, it.Serial)
	return nil
}
