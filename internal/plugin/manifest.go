// Package plugin runs installed, sandboxed plugins. A plugin is an artifact
// (plugin.js on the shared embedded QuickJS, or plugin.wasm) plus a sidecar
// plugin.json manifest that declares its ceiling (the capabilities × hosts it may
// attempt), the tools it exposes, and the credentials it uses. The manifest is
// read and reviewed WITHOUT running the artifact; the broker enforces the ceiling
// regardless of what the artifact tries. The plugin's code runs in the wazero
// sandbox and reaches effects only through the one gate — bounded by its ceiling,
// gated by the broker + HITL, and by the user's context-scoped grants.
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

	"github.com/efuturetoday/nocturn/internal/capability"
)

// Manifest is a plugin's static declaration (sidecar plugin.json).
type Manifest struct {
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Tools       []ToolDecl       `json:"tools"`
	Requires    []Require        `json:"requires"`    // the ceiling: reach (family + target) × mutation
	Credentials []CredentialDecl `json:"credentials"` // host-injected credentials it uses
	OAuth       []OAuthDecl      `json:"oauth"`       // OAuth providers the host runs on its behalf
}

// OAuthDecl declares an OAuth2 provider the plugin needs — so the plugin brings
// its own provider instead of the host hard-coding every one. The host runs the
// authorization-code (+PKCE) flow at install time, holds and refreshes the token,
// and injects it as the credential named Name (which must match a CredentialDecl).
// The guest never sees the token. client_id is a public/PKCE client id (shipped by
// the plugin author, registered with the provider); no client_secret is needed.
type OAuthDecl struct {
	Name     string `json:"name"`      // links to a CredentialDecl.Name (the injected secret)
	AuthURL  string `json:"auth_url"`  // https authorization endpoint
	TokenURL string `json:"token_url"` // https token endpoint
	ClientID string `json:"client_id"`
	// ClientSecret is the client secret for providers whose token endpoint
	// requires one (e.g. Google "Desktop app"/"Web" clients — even with PKCE).
	// Empty = public/PKCE client, no secret. For a DESKTOP-app client the secret
	// is explicitly non-confidential (Google embeds one in every gcloud copy), so
	// the plugin author ships it here and every user just consents — nobody
	// registers their own app.
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
}

// ToolDecl declares a tool the plugin exposes to the model. Intent is an optional
// human-readable template for the HITL prompt, with {field} placeholders filled
// from the call's arguments (e.g. "Send an email to {to}"). It is reviewed at
// install time and shown at the semantic level instead of the transport effect
// the tool performs underneath; empty means the effect tool's own wording is used.
type ToolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON-schema object
	Intent      string          `json:"intent"`     // optional HITL template, {field} placeholders
	// Consequential marks a tool as irreversible/high-stakes (delete, pay, send).
	// It is the never-auto floor: such a tool ALWAYS asks out-of-band — it can never
	// be covered by a standing grant nor be granted "session"/"always" (the moat).
	// Install-reviewed and trusted (manifest, not guest code).
	Consequential bool `json:"consequential"`
}

// Require is one reach entry of a plugin's ceiling: a Family (the host primitive —
// "http", "file", "dns") + Target (a host for http, a path glob for file) and the
// access level. Mutates=true grants read AND write (write is the higher privilege —
// if you may write a target you may certainly read it); Mutates=false is read-only.
type Require struct {
	Family  string `json:"family"`
	Target  string `json:"target"`
	Mutates bool   `json:"mutates"`
}

// CredentialDecl declares a credential the host injects for the plugin (never seen
// by the plugin), mirroring secret.Binding. Family scopes it to a host primitive
// ("http"); a bearer is action-agnostic (injected on both reads and writes).
type CredentialDecl struct {
	Name   string `json:"name"`
	Family string `json:"family"`
	Host   string `json:"host"`
	Header string `json:"header"`
	Prefix string `json:"prefix"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Validate rejects a malformed manifest fail-closed: an empty/odd name, no tools,
// duplicate tool names, a non-object schema, or a requires/credential entry with
// an empty capability or host (which would fail-closed later anyway, but is
// rejected early so the operator never reviews a nonsensical ceiling).
func (m Manifest) Validate() error {
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("plugin: invalid name %q (want ^[a-z0-9][a-z0-9._-]*$)", m.Name)
	}
	if m.Version == "" {
		return errors.New("plugin: empty version")
	}
	if len(m.Tools) == 0 {
		return errors.New("plugin: no tools declared")
	}
	seen := map[string]bool{}
	for _, t := range m.Tools {
		if t.Name == "" {
			return errors.New("plugin: empty tool name")
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
	for _, r := range m.Requires {
		if r.Family == "" || r.Target == "" {
			return fmt.Errorf("plugin: requires entry needs a family and target (got %q, %q)", r.Family, r.Target)
		}
	}
	ceil := m.Ceiling()
	creds := map[string]bool{}
	for _, c := range m.Credentials {
		if c.Name == "" || c.Family == "" || c.Host == "" || c.Header == "" {
			return fmt.Errorf("plugin: credential %q needs name, family, host and header", c.Name)
		}
		// A credential is only ever injected on an effect the ceiling permits; one
		// whose (family, host) is unreachable in the ceiling could never be used, and
		// a mismatch is a red flag (a credential quietly pointed at another host than
		// the ceiling allows). A bearer injects on both reads and writes, so it is
		// coherent as long as the host is reachable in EITHER class.
		if !ceil.Allows(capability.Call{Family: c.Family, Mutates: false, Target: c.Host}) &&
			!ceil.Allows(capability.Call{Family: c.Family, Mutates: true, Target: c.Host}) {
			return fmt.Errorf("plugin: credential %q (%s %s) is outside the requires ceiling", c.Name, c.Family, c.Host)
		}
		creds[c.Name] = true
	}
	for _, o := range m.OAuth {
		if o.Name == "" || o.ClientID == "" || len(o.Scopes) == 0 {
			return fmt.Errorf("plugin: oauth %q needs a name, client_id and at least one scope", o.Name)
		}
		if !isHTTPSURL(o.AuthURL) || !isHTTPSURL(o.TokenURL) {
			return fmt.Errorf("plugin: oauth %q auth_url and token_url must be https URLs", o.Name)
		}
		if !creds[o.Name] {
			// The token has to be injected SOMEWHERE — an oauth block with no matching
			// credential would fetch a token nothing ever uses.
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

// Ceiling builds the plugin's upper bound from its Requires: each entry becomes an
// Allow reach on (family, target). A write entry grants read+write (MatchAny — write
// implies read); a read entry is read-only (MatchRead).
func (m Manifest) Ceiling() capability.Ceiling {
	pairs := make([]capability.Pair, 0, len(m.Requires))
	for _, r := range m.Requires {
		writes := capability.MatchRead
		if r.Mutates {
			writes = capability.MatchAny
		}
		pairs = append(pairs, capability.Pair{Family: r.Family, TargetGlob: r.Target, Writes: writes})
	}
	return capability.NewCeiling(pairs...)
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

// Load reads dir/plugin.json (validated) and the artifact (plugin.wasm XOR
// plugin.js) WITHOUT executing anything — so the operator reviews the declared
// ceiling before any plugin code runs.
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
