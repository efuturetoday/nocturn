// Package plugin runs installed, sandboxed plugins as ordinary model tools. A plugin is an artifact
// (plugin.js on the shared embedded QuickJS, or plugin.wasm) plus a sidecar plugin.json manifest that
// declares the tools it exposes, which base tools its guest may call (its cage), and the credentials
// the host injects for it. The manifest is read and reviewed WITHOUT running the artifact.
//
// A plugin's cage is a TOOLSET, not a bespoke boundary: `uses` names the base tools its guest may
// dispatch to, exactly as an agent's tool filter scopes a sub-agent. Everything past that is the
// ordinary gate — a host is approved by the human (HITL) per request, not by a static per-plugin
// target list. So a plugin never holds more authority than a subset of the base tools, each still
// gated the same way the model's own calls are.
package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Manifest is a plugin's static declaration (sidecar plugin.json).
type Manifest struct {
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Tools       []ToolDecl       `json:"tools"`
	Uses        []string         `json:"uses"`        // base tool names the guest may call ("*" = all); its cage
	Credentials []CredentialDecl `json:"credentials"` // host-injected credentials it uses
}

// ToolDecl declares a tool the plugin exposes to the model.
type ToolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON-schema object
}

// CredentialDecl declares a credential the host injects for the plugin (never seen by the plugin),
// mirroring secret.Binding. Host is the sole scoping dimension — a request credential is inherently a
// network credential, so the host IS the discriminator; a bearer is injected on any request to it.
type CredentialDecl struct {
	Name   string `json:"name"`
	Host   string `json:"host"`
	Header string `json:"header"`
	Prefix string `json:"prefix"`
}

// nameRe bounds a plugin name AND a tool name: no dots, because a tool is exposed to the model as
// <plugin>_<tool>, which must match a strict tool-call provider's ^[a-zA-Z0-9_-]{1,64}$.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Validate rejects a malformed manifest fail-closed: a bad name, no tools, duplicate/odd tool names,
// a non-object schema, or a credential entry with an empty field.
func (m Manifest) Validate() error {
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("plugin: invalid name %q (want %s, no dots)", m.Name, nameRe)
	}
	if m.Version == "" {
		return errors.New("plugin: empty version")
	}
	if len(m.Tools) == 0 {
		return errors.New("plugin: no tools declared")
	}
	seen := map[string]bool{}
	for _, t := range m.Tools {
		if !nameRe.MatchString(t.Name) {
			return fmt.Errorf("plugin: invalid tool name %q (want %s, no dots)", t.Name, nameRe)
		}
		if seen[t.Name] {
			return fmt.Errorf("plugin: duplicate tool %q", t.Name)
		}
		seen[t.Name] = true
		var obj map[string]any
		if json.Unmarshal(t.Parameters, &obj) != nil || obj["type"] != "object" {
			return fmt.Errorf("plugin: tool %q parameters must be a JSON object schema", t.Name)
		}
	}
	for _, c := range m.Credentials {
		if c.Name == "" || c.Host == "" || c.Header == "" {
			return fmt.Errorf("plugin: credential %q needs name, host and header", c.Name)
		}
	}
	return nil
}

// uses reports whether the guest may dispatch to a base tool of the given name: an exact match in the
// manifest's Uses list, or a bare "*" that admits all base tools. Empty Uses = a pure-compute plugin
// (its guest can call nothing).
func (m Manifest) allows(tool string) bool {
	for _, u := range m.Uses {
		if u == "*" || u == tool {
			return true
		}
	}
	return false
}

// Kind is a plugin artifact form.
type Kind int

const (
	KindJS   Kind = iota // plugin.js, run on the shared embedded QuickJS interpreter
	KindWASM             // plugin.wasm, a wasm32-wasi guest run directly
)

// Loaded is a validated plugin package: manifest + artifact + kind.
type Loaded struct {
	Manifest Manifest
	Artifact []byte
	Kind     Kind
}

// Load reads dir/plugin.json (validated) and the artifact (plugin.wasm XOR plugin.js) WITHOUT
// executing anything.
func Load(dir string) (Loaded, error) {
	data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return Loaded{}, fmt.Errorf("plugin: read manifest: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Loaded{}, fmt.Errorf("plugin: parse manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Loaded{}, err
	}

	wasm, js := filepath.Join(dir, "plugin.wasm"), filepath.Join(dir, "plugin.js")
	hasWASM, hasJS := fileExists(wasm), fileExists(js)
	switch {
	case hasWASM && hasJS:
		return Loaded{}, errors.New("plugin: both plugin.wasm and plugin.js present — pick one")
	case hasWASM:
		b, err := os.ReadFile(wasm)
		return Loaded{Manifest: m, Artifact: b, Kind: KindWASM}, err
	case hasJS:
		b, err := os.ReadFile(js)
		return Loaded{Manifest: m, Artifact: b, Kind: KindJS}, err
	default:
		return Loaded{}, errors.New("plugin: no plugin.wasm or plugin.js in " + dir)
	}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
