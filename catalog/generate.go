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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/efuturetoday/nocturn/internal/frontmatter"
	"github.com/efuturetoday/nocturn/internal/library"
	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/plugin"
)

// out is where the generated catalog goes: docs/public is copied to the site root by Astro, so this
// is what the docs workflow publishes.
const out = "../docs/public/catalog.json"

// entry is the listing half of a skill — everything that is NOT in the SKILL.md, because the
// SKILL.md is installed verbatim and must not carry shop metadata.
type entry struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"` // defaults to the frontmatter description
	Homepage    string   `json:"homepage,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Serial is a plugin's revision, and only a plugin's: it is inside the signed statement, so it
	// only goes up and it is re-signed when it moves. Bump it when publishing a change — a daemon
	// that has seen a higher one refuses to go back to this.
	Serial int `json:"serial,omitempty"`
}

// server is one remote MCP declaration plus how it is listed. The declaration half is the same shape
// mcp.Server has, so what the catalog offers and what lands in mcp.json cannot drift.
type server struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Homepage    string         `json:"homepage,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	URL         string         `json:"url"`
	Auth        string         `json:"auth,omitempty"`
	OAuth       *mcp.OAuthDecl `json:"oauth,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
		os.Exit(1)
	}
}

func run() error {
	skills, err := readSkills("skills")
	if err != nil {
		return err
	}
	servers, err := readServers("mcp")
	if err != nil {
		return err
	}
	plugins, err := readPlugins("plugins")
	if err != nil {
		return err
	}
	cat := library.Catalog{
		SchemaVersion: 1,
		Skills:        skills,
		MCP:           servers,
		Plugins:       plugins,
	}
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
	fmt.Printf("catalog: %d skills, %d servers, %d plugins → %s\n", len(skills), len(servers), len(plugins), out)
	return nil
}

// readPlugins turns plugins/<folder>/ into catalog entries, sorted by id. Each folder holds what an
// install writes — plugin.json and plugin.js — plus the listing metadata, which stays OUT of the
// manifest: the manifest is copied verbatim to disk, and shop wording has no business being read back
// by the loader.
func readPlugins(dir string) ([]library.PluginItem, error) {
	dirs, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []library.PluginItem
	for _, d := range dirs {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, d.Name())
		manifest, err := os.ReadFile(filepath.Join(path, plugin.ManifestFile))
		if err != nil {
			return nil, err
		}
		script, err := os.ReadFile(filepath.Join(path, plugin.ScriptFile))
		if err != nil {
			return nil, err
		}
		// The bundled skill is optional: a plugin whose tools explain themselves needs none, and one
		// nobody needed would cost prompt space in every turn of every conversation.
		bundled, err := os.ReadFile(filepath.Join(path, plugin.SkillFile))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		var e entry
		if err := readJSON(filepath.Join(path, "entry.json"), &e); err != nil {
			return nil, err
		}
		if e.Title == "" || e.Description == "" {
			return nil, fmt.Errorf("plugins/%s/entry.json: title and description are required", d.Name())
		}
		// The manifest is parsed here as well as by the daemon, so a plugin that could not be
		// installed is a build failure rather than an entry silently missing from the shelf.
		var m plugin.Manifest
		if err := json.Unmarshal(manifest, &m); err != nil {
			return nil, fmt.Errorf("plugins/%s/%s: %w", d.Name(), plugin.ManifestFile, err)
		}
		if m.Name == "" {
			m.Name = d.Name()
		}
		if err := m.Validate(); err != nil {
			return nil, err
		}
		// The signature is committed beside the plugin rather than produced here: a generator that
		// signed would need the private key, which would put it in CI, which is the one place it must
		// not be. Missing is a build failure — an unsigned plugin is refused by every daemon, so
		// publishing one would be publishing an entry that cannot be installed.
		sig, err := os.ReadFile(filepath.Join(path, "plugin.sig"))
		if err != nil {
			return nil, fmt.Errorf("plugins/%s: no signature (run `go run sign.go %s`): %w", d.Name(), d.Name(), err)
		}
		if e.Serial < 1 {
			return nil, fmt.Errorf("plugins/%s/entry.json: serial must be at least 1, and must be bumped when you publish a change", d.Name())
		}
		out = append(out, library.PluginItem{
			ID:          d.Name(),
			Title:       e.Title,
			Description: e.Description,
			Homepage:    e.Homepage,
			Tags:        e.Tags,
			Folder:      d.Name(),
			Manifest:    string(manifest),
			Script:      string(script),
			Skill:       string(bundled),
			ManifestSHA: digest(manifest),
			ScriptSHA:   digest(script),
			SkillSHA:    skillDigest(bundled),
			Serial:      e.Serial,
			Signature:   strings.TrimSpace(string(sig)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// skillDigest is the digest of an OPTIONAL file: absent stays empty rather than becoming the digest
// of nothing, which is a real hash and would claim a skill exists.
func skillDigest(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return digest(b)
}

// digest is the sha256 the daemon recomputes over the same bytes.
func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readSkills turns skills/<folder>/ into catalog entries, sorted by id so the output is stable.
func readSkills(dir string) ([]library.SkillItem, error) {
	dirs, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []library.SkillItem
	for _, d := range dirs {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		it, err := readSkill(filepath.Join(dir, d.Name()), d.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// readSkill builds one entry. The folder name IS the id and the install folder: one identity, so a
// renamed directory cannot leave a stale id behind pointing at it.
func readSkill(path, name string) (library.SkillItem, error) {
	body, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
	if err != nil {
		return library.SkillItem{}, err
	}
	meta, _, err := frontmatter.Parse(body)
	if err != nil {
		return library.SkillItem{}, fmt.Errorf("%s/SKILL.md: %w", name, err)
	}
	var e entry
	if err := readJSON(filepath.Join(path, "entry.json"), &e); err != nil {
		return library.SkillItem{}, err
	}
	if e.Title == "" {
		return library.SkillItem{}, fmt.Errorf("%s/entry.json: title is required", name)
	}
	// The listing description falls back to the frontmatter one rather than being repeated: two
	// copies of a sentence are two sentences to keep in step, and only one of them is ever tested.
	desc := e.Description
	if desc == "" {
		desc = strings.TrimSpace(meta.Description)
	}
	return library.SkillItem{
		ID:          name,
		Title:       e.Title,
		Description: desc,
		Homepage:    e.Homepage,
		Tags:        e.Tags,
		Folder:      name,
		Body:        string(body),
		SHA256:      digest(body),
	}, nil
}

// readServers turns mcp/<name>.json into catalog entries, sorted by id.
func readServers(dir string) ([]library.MCPItem, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []library.MCPItem
	for _, f := range files {
		name, ok := strings.CutSuffix(f.Name(), ".json")
		if f.IsDir() || !ok || strings.HasPrefix(f.Name(), ".") || strings.HasPrefix(f.Name(), "_") {
			continue
		}
		var s server
		if err := readJSON(filepath.Join(dir, f.Name()), &s); err != nil {
			return nil, err
		}
		// The same Validate the loader and the daemon run, so a declaration that could not be
		// installed is a build failure here rather than an entry that silently never appears.
		decl := mcp.Server{Name: name, URL: s.URL, Auth: s.Auth, OAuth: s.OAuth}
		if err := decl.Validate(); err != nil {
			return nil, fmt.Errorf("mcp/%s: %w", f.Name(), err)
		}
		out = append(out, library.MCPItem{
			ID:          name,
			Title:       s.Title,
			Description: s.Description,
			Homepage:    s.Homepage,
			Tags:        s.Tags,
			Name:        name,
			URL:         s.URL,
			Auth:        s.Auth,
			OAuth:       s.OAuth,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// readJSON decodes one metadata file strictly — an unknown key is a typo somebody meant to matter.
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// revision is the catalog's own version string: a digest of what it contains.
//
// A date would be the obvious choice and is the wrong one — it changes when nothing did, so the
// generated file would differ from the committed one every day and the CI drift check would cry wolf.
// A content digest changes exactly when the catalog changes, which is what a client showing a version
// wants to know anyway.
func revision(cat library.Catalog) string {
	cat.Version = ""
	// The whole document, not a hand-picked set of fields: the first version hashed ids and artifact
	// digests, so renaming an entry changed what is published and left the version saying otherwise.
	// A digest of everything cannot drift from what it describes.
	data, err := json.Marshal(cat)
	if err != nil {
		panic("catalog: revision: " + err.Error()) // a catalog that will not marshal cannot be written either
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}
