package extension

import (
	"fmt"
	"os"
	"path/filepath"
)

// Package is everything one install writes: the shared declaration and whichever payloads the thing
// carries. A field left empty is a payload it does not have.
type Package struct {
	Name           string
	Manifest       string // manifest.json — the config it needs and the credentials it wants
	Skill          string // SKILL.md
	PluginManifest string // plugin.json
	PluginScript   string // plugin.js
	MCP            string // mcp.json
}

// Install writes a package into its own folder under root.
//
// It REFUSES an existing folder rather than merging into it. Merging would mean an install could add
// a payload to something already installed — a plugin dropped beside a skill somebody trusts, sharing
// its folder, its owner and its credential — which is a change of authority wearing the clothes of an
// update.
//
// The declaration is written FIRST and every announcing file last — see the order below.
func Install(root string, p Package) error {
	if !ValidName(p.Name) {
		return fmt.Errorf("%q is not a valid extension name", p.Name)
	}
	if p.Skill == "" && p.MCP == "" && (p.PluginManifest == "" || p.PluginScript == "") {
		return fmt.Errorf("%s carries nothing installable", p.Name)
	}
	dir := filepath.Join(root, p.Name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s is already installed", p.Name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	write := func(name, body string) error {
		if body == "" {
			return nil
		}
		return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	// Order matters: what makes a folder DISCOVERABLE as a payload goes last. Discovery keys on
	// plugin.json, mcp.json and SKILL.md, so each is written after everything it needs — plugin.js
	// before the manifest that announces it, the declaration before any of them. A crash midway then
	// leaves a folder that is not discovered at all, rather than one announcing a payload whose
	// artifact never arrived.
	for _, part := range []struct{ name, body string }{
		{ManifestFile, p.Manifest},
		{"plugin.js", p.PluginScript},
		{"plugin.json", p.PluginManifest},
		{"mcp.json", p.MCP},
		{"SKILL.md", p.Skill},
	} {
		if err := write(part.name, part.body); err != nil {
			// A half-written folder is worse than none: it would be discovered as whatever landed
			// first. The folder was created by this call, so removing it takes nothing else with it.
			_ = os.RemoveAll(dir)
			return err
		}
	}
	return nil
}

// Remove deletes an installed extension's folder, its shard with it.
//
// No trash, and that is the same trade skills always made: what is lost is instructions, code and a
// declaration that came from somewhere and can come from there again. What is NOT lost quietly is the
// standing permission its credential's host holds — the caller revokes that before calling here,
// because the declaration naming the host only exists until this returns.
func Remove(root, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%q is not a valid extension name", name)
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no extension %q", name)
	}
	return os.RemoveAll(dir)
}
