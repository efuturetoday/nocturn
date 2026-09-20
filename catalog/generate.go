//go:build ignore

// Command generate builds docs/public/catalog.json from the tree beside it.
//
// Run it with `go generate ./catalog/`. The output is committed, and CI regenerates it and fails on a
// diff — the same arrangement the .gsx templates have, for the same reason: a generated file that
// nobody regenerates is a file that disagrees with its source.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/efuturetoday/nocturn/internal/library"
)

// out is where the generated catalog goes: docs/public is copied to the site root by Astro, so this
// is what the docs workflow publishes.
const out = "../docs/public/catalog.json"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
		os.Exit(1)
	}
}

func run() error {
	items, err := readItems(src)
	if err != nil {
		return err
	}
	cat := library.Catalog{SchemaVersion: 1, Items: items}
	cat.Version = revision(cat)
	data, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("catalog: %d entries → %s\n", len(items), out)
	return nil
}

// revision is the catalog's own version: a digest over every entry's identity, serial and content
// digest, truncated for readability.
//
// Derived from the CONTENT rather than from counts, because a count and a highest serial do not move
// when an entry changes — which is precisely the moment a client comparing versions needs to notice.
// It is not a security property: each entry carries its own signature and its own serial, and this
// string is only how a client asks "is there anything new".
func revision(cat library.Catalog) string {
	h := sha256.New()
	for _, it := range cat.Items {
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", it.ID, it.Serial, it.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
