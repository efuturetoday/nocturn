//go:build ignore

// Reading the catalog source tree, shared by generate.go and sign.go.
//
// One reader, because what is SIGNED must be what is PUBLISHED: a second reader here would be a
// second opinion about what the bytes are, and the signature would then vouch for whichever of the
// two was wrong. Run both tools with this file: `go run generate.go entryread.go`.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/efuturetoday/nocturn/internal/frontmatter"
	"github.com/efuturetoday/nocturn/internal/library"
)

// src is the tree entries are authored in: one folder per installable thing, holding exactly what an
// install writes plus the listing beside it.
const src = "extensions"

// sigFile is where `go run sign.go` writes an entry's signature. It sits beside the payloads and is
// committed, so CI never holds the key.
const sigFile = "extension.sig"

// entry is the listing half — everything that is NOT installed, because the payloads are written
// verbatim and must not carry shop metadata.
type entry struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"` // defaults to the frontmatter description
	Homepage    string   `json:"homepage,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Serial is this entry's revision. It is inside the signed statement, so it only goes up and is
	// re-signed when it moves. Bump it when publishing a change — a daemon that has seen a higher one
	// refuses to go back to this.
	Serial int `json:"serial"`
}

// readItems turns extensions/<name>/ into catalog entries, sorted by id so the output is stable.
//
// An UNSIGNED entry is not published. A daemon refuses one from a remote catalog, so publishing it
// would put something on the shelf that every remote client silently drops — a shop full of things
// nobody can take home. The message names the missing file rather than the rule, because the fix is
// one command.
func readItems(dir string) ([]library.Item, error) {
	dirs, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []library.Item
	for _, d := range dirs {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") {
			continue
		}
		it, err := readItem(filepath.Join(dir, d.Name()), d.Name())
		if err != nil {
			return nil, err
		}
		if it.Signature == "" {
			fmt.Printf("catalog: %s not published (no %s — sign it: go run sign.go entryread.go -key …)\n", d.Name(), sigFile)
			continue
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// readItem builds one entry. The folder name IS the id and the install folder: one identity, so a
// renamed directory cannot leave a stale id behind pointing at it.
func readItem(path, name string) (library.Item, error) {
	it := library.Item{ID: name}

	var err error
	if it.Manifest, err = optional(filepath.Join(path, "manifest.json")); err != nil {
		return library.Item{}, err
	}
	if it.Skill, err = optional(filepath.Join(path, "SKILL.md")); err != nil {
		return library.Item{}, err
	}
	if it.PluginManifest, err = optional(filepath.Join(path, "plugin.json")); err != nil {
		return library.Item{}, err
	}
	if it.PluginScript, err = optional(filepath.Join(path, "plugin.js")); err != nil {
		return library.Item{}, err
	}
	if it.MCP, err = optional(filepath.Join(path, "mcp.json")); err != nil {
		return library.Item{}, err
	}

	var e entry
	if err := readJSON(filepath.Join(path, "entry.json"), &e); err != nil {
		return library.Item{}, err
	}
	if e.Title == "" {
		return library.Item{}, fmt.Errorf("%s/entry.json: title is required", name)
	}
	if e.Serial < 1 {
		return library.Item{}, fmt.Errorf("%s/entry.json: serial must be at least 1, and must be bumped when you publish a change", name)
	}
	// The listing description falls back to the skill's frontmatter rather than being repeated: two
	// copies of a sentence are two sentences to keep in step, and only one of them is ever tested.
	desc := e.Description
	if desc == "" && it.Skill != "" {
		meta, _, err := frontmatter.Parse([]byte(it.Skill))
		if err != nil {
			return library.Item{}, fmt.Errorf("%s/SKILL.md: %w", name, err)
		}
		desc = strings.TrimSpace(meta.Description)
	}
	it.Title, it.Description, it.Homepage, it.Tags, it.Serial = e.Title, desc, e.Homepage, e.Tags, e.Serial
	it.SHA256 = library.ItemDigest(it)

	sig, err := optional(filepath.Join(path, sigFile))
	if err != nil {
		return library.Item{}, err
	}
	it.Signature = strings.TrimSpace(sig)
	return it, nil
}

// optional reads a file that may not be there — a payload an entry does not carry.
func optional(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// readJSON decodes one JSON file strictly: an unknown field is a mistake in the source tree, and a
// silently ignored one would publish something the author did not mean.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
