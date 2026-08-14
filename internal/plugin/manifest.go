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
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Manifest is a plugin's static declaration (sidecar plugin.json).
type Manifest struct {
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Tools       []ToolDecl       `json:"tools"`
	Uses        []string         `json:"uses"`        // base tool names the guest may call ("*" = all); its cage
	Credentials []CredentialDecl `json:"credentials"` // host-injected credentials it uses
	OAuth       []OAuthDecl      `json:"oauth"`       // OAuth2 providers the host runs on its behalf
}

// OAuthDecl declares an OAuth2 provider the plugin needs — so the plugin brings its own instead of the
// host hard-coding every one. The host runs the authorization-code (+PKCE) flow via `nocturn auth
// <name>`, holds and refreshes the token, and injects it as the credential named Name (which must
// match a CredentialDecl). The guest never sees the token. ClientSecret may be "" for a public/PKCE
// client; a desktop-app client's secret is non-confidential and shipped in the manifest.
type OAuthDecl struct {
	Name         string   `json:"name"` // links to a CredentialDecl.Name (the injected secret)
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
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
	credentialNames := map[string]bool{}
	for _, c := range m.Credentials {
		if c.Name == "" || c.Host == "" || c.Header == "" {
			return fmt.Errorf("plugin: credential %q needs name, host and header", c.Name)
		}
		credentialNames[c.Name] = true
	}
	for _, o := range m.OAuth {
		// client_id may be empty, and that is not laxness: a plugin for a provider whose scopes are
		// restricted (Gmail is one) CANNOT ship a shared client — Google requires an annual
		// third-party security assessment for one, and every household's mail would run through a
		// single project. Such a plugin declares the endpoints and the scopes, and the person supplies
		// their own client once with `nocturn auth <plugin> --client-id …`, which stores it in the
		// plugin's shard beside the token. Everything else stays required: without a name, scopes and
		// https endpoints there is no flow to run at all.
		if o.Name == "" || len(o.Scopes) == 0 {
			return fmt.Errorf("plugin: oauth %q needs a name and at least one scope", o.Name)
		}
		if !isHTTPSURL(o.AuthURL) || !isHTTPSURL(o.TokenURL) {
			return fmt.Errorf("plugin: oauth %q auth_url and token_url must be https URLs", o.Name)
		}
		// The token has to be injected somewhere — an oauth block with no matching credential of the
		// same name would fetch a token nothing ever uses.
		if !credentialNames[o.Name] {
			return fmt.Errorf("plugin: oauth %q has no matching credential of the same name", o.Name)
		}
	}
	return nil
}

// isHTTPSURL reports whether s is a well-formed https:// URL with a host.
func isHTTPSURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// allows reports whether the guest may dispatch to a base tool of the given name: an exact match in the
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

// Loaded is a validated plugin package: manifest + artifact + kind, and the skill it bundles if it
// brought one.
//
// A plugin answers two different questions and they belong in two different files. The manifest says
// what it MAY do and the artifact says HOW; neither says WHEN the assistant should reach for it, or
// which query syntax its search takes, or that reading a body costs context nobody asked to spend.
// That is skill material — instructions, no authority — so a plugin may ship a SKILL.md beside its
// code, and the workspace folds it into the same skill catalog a hand-written one lands in.
type Loaded struct {
	Manifest Manifest
	Artifact []byte
	Kind     Kind
	Skill    string // the bundled SKILL.md, verbatim; empty when there is none
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
	// The name defaults to the plugin's folder; a manifest name may override it (the shared
	// discovery rule — Discover warns on a mismatch). Identity is the folder either way.
	if m.Name == "" {
		m.Name = filepath.Base(dir)
	}
	if err := m.Validate(); err != nil {
		return Loaded{}, err
	}

	// A bundled skill is optional and is NOT parsed here: this package owns the manifest format, and
	// the skill format belongs to internal/skill. An unreadable one is left empty rather than failing
	// the load — the tools are the plugin, the skill is advice about them.
	bundled, _ := os.ReadFile(filepath.Join(dir, SkillFile))

	wasm, js := filepath.Join(dir, "plugin.wasm"), filepath.Join(dir, ScriptFile)
	hasWASM, hasJS := fileExists(wasm), fileExists(js)
	switch {
	case hasWASM && hasJS:
		return Loaded{}, errors.New("plugin: both plugin.wasm and plugin.js present — pick one")
	case hasWASM:
		b, err := os.ReadFile(wasm)
		if err != nil {
			return Loaded{}, fmt.Errorf("plugin: read artifact: %w", err)
		}
		return Loaded{Manifest: m, Artifact: b, Kind: KindWASM, Skill: string(bundled)}, nil
	case hasJS:
		b, err := os.ReadFile(js)
		if err != nil {
			return Loaded{}, fmt.Errorf("plugin: read artifact: %w", err)
		}
		return Loaded{Manifest: m, Artifact: b, Kind: KindJS, Skill: string(bundled)}, nil
	default:
		return Loaded{}, errors.New("plugin: no plugin.wasm or plugin.js in " + dir)
	}
}

// SkillBodies returns the SKILL.md of every plugin under root, keyed by folder.
//
// A file scan, deliberately separate from Discover: the workspace needs these BEFORE it builds the
// tool set (the skill catalog is assembled first, and a plugin's guest is compiled with the base
// tools that come out of it), and reading two files is cheaper than reordering that assembly around
// a dependency that does not really exist.
func SkillBodies(root string) map[string]string {
	entries, _ := os.ReadDir(root)
	out := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		// Only for something that is actually a plugin: a stray directory holding a SKILL.md is not
		// one, and folding its text into every prompt because it sits under plugins/ would be a way
		// to get into the system prompt without being anything.
		if !fileExists(filepath.Join(root, e.Name(), "plugin.json")) {
			continue
		}
		if body, err := os.ReadFile(filepath.Join(root, e.Name(), SkillFile)); err == nil && len(body) > 0 {
			out[e.Name()] = string(body)
		}
	}
	return out
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
