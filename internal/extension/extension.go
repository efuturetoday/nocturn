// Package extension is what a workspace installs, and the one declaration every installed thing
// shares.
//
// ONE TREE. Everything installed lives in extensions/<name>/, and what a folder IS gets read off the
// files in it rather than from which directory it sits in:
//
//	<workspace>/extensions/<name>/
//	    SKILL.md        instructions the model reads          (optional)
//	    plugin.json     + plugin.js — code the sandbox runs   (optional)
//	    mcp.json        a remote server the host dials        (optional)
//	    manifest.json   the declaration — installed verbatim, never edited by hand
//	    config.json     the values a human supplied — typed, validated, never names or hosts
//	    secrets.enc     the credential values, path-bound (secret.OpenShard)
//
// A folder may carry SEVERAL payloads, and that is the point: the unit a household installs is a
// capability — "Home Assistant", "GitHub" — not the mechanism it happens to arrive through. Split
// across three trees, one integration became three installs with three owners and three credentials,
// and somebody could take the server and leave behind the instructions that say when to use it.
//
// What a payload IS still decides who vouches for it — text over TLS, code and declarations under an
// Ed25519 signature, a remote host behind the NetKind gate — and it still decides how it runs. That
// is the one axis the payloads legitimately differ on. Everything in this package is the same for all
// of them.
//
// # Manifest against config
//
// The split is the security boundary, and it is the general form of the rule internal/mail states for
// itself: a configuration that could NAME its secret would let whoever edits the file point the
// credential somewhere else. So the manifest names the credential and the host; the config only ever
// fills in values the manifest declared, each one typed and validated. A value can steer WHICH host a
// declared credential is bound to (a household's own server has no address the catalog could know),
// but it can never invent a credential, a header, an audience or an owner.
//
// Before this package there were four spellings of "what do I need, and who owns the secret": a
// plugin declared it in its manifest, an MCP server derived it from its URL, a skill could not
// express it at all, and the workspace had a hand-written bindings.json plus a NOCTURN_SECRET_*
// environment channel beside it. Four answers to one question is how a rule stops being a rule.
package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/efuturetoday/nocturn/internal/discovery"
	"github.com/efuturetoday/nocturn/internal/secret"
)

const (
	// ManifestFile is the declaration inside an extension folder. It is written by the install and
	// read back by the daemon; nothing a human types goes in here.
	ManifestFile = "manifest.json"

	// ConfigFile is the values a human supplied for this extension's declared config.
	ConfigFile = "config.json"

	// maxValueBytes caps one config value. A value is substituted into text the model reads on every
	// turn, so its length is part of the prompt budget, not just of the file.
	maxValueBytes = 512

	// maxManifestBytes caps a manifest read — a declaration is small by construction.
	maxManifestBytes = 64 << 10
)

// Dir is the one workspace subdirectory installed things live in.
//
// One tree rather than skills/, plugins/ and mcp/ side by side, because the unit a household thinks
// in is a capability — "Home Assistant", "GitHub" — and not the mechanism it happens to be delivered
// through. Splitting one integration across three folders made three installs, three owners and three
// credentials out of one thing, and let somebody install the server and forget the instructions that
// say when to use it.
const Dir = "extensions"

// Payload is what an extension carries. A folder may carry several: what makes a thing a skill is a
// SKILL.md in it, not which directory it sits in.
//
// The kinds are still real where they were always real — a skill is text in the prompt, a plugin is
// code in the sandbox, a server is a foreign host behind the gate, and each answers to its own trust
// rule. What is gone is the idea that a kind is a KIND OF INSTALL.
type Payload string

const (
	PayloadSkill  Payload = "skill"  // SKILL.md — instructions the model reads
	PayloadPlugin Payload = "plugin" // plugin.json + plugin.js/wasm — code the sandbox runs
	PayloadMCP    Payload = "mcp"    // mcp.json — a remote server the host dials
)

// declRe bounds a config or credential NAME inside a declaration — not the extension's own name,
// which is discovery's rule and is not restated here.
var declRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidName reports whether s can be an extension name. It is discovery's rule, unchanged: an
// extension IS a discoverable item, and a second spelling of "what may a name look like" is how one
// layer comes to accept what the next skips.
func ValidName(s string) bool { return discovery.ValidName(s) }

// Owner is the credential-injection owner id: "ext:<name>", the folder's name.
//
// One owner per installed thing, whatever it carries. A folder holding a server declaration AND the
// skill that explains it is ONE extension with one credential — which is the whole reason the tree was
// collapsed; two owners for one integration is what made "which of these three do I revoke" a
// question nobody could answer.
func Owner(name string) string { return "ext:" + name }

// SecretName is the vault key a credential is stored and injected under:
// "<owner>@<host>/<credential>", host lowercased.
//
// The host is IN the key, and that is a security boundary rather than a decoration: point the same
// extension at a different host — by editing its config, or by re-installing it against another
// server — and the key changes, so the credential issued for the old host is simply not found. A
// token never rides along to a host nobody issued it for. Same host, same key, so the value survives
// restarts and reloads. A credential with no host (one the host code reads itself rather than
// injecting into a request) drops the "@host" half.
func SecretName(owner, host, cred string) string {
	if host == "" {
		return owner + "/" + cred
	}
	return owner + "@" + strings.ToLower(host) + "/" + cred
}

// Decl is what an extension declares about itself. Every kind's manifest embeds it, which is what
// makes "this needs a token before it works" one fact with one shape rather than a field somebody
// added to one manifest.
type Decl struct {
	// Config is what a human must supply. Ordered — a client renders the form in this order.
	Config []ConfigDecl `json:"config,omitempty"`
	// Credentials are the secrets the host holds on this extension's behalf. The guest, the model
	// and the config never see a value.
	Credentials []CredentialDecl `json:"credentials,omitempty"`
}

// ConfigDecl is one value a human supplies. Type is not decoration: the value is substituted into
// text the model reads, so an unvalidated field would be a prompt-injection channel with the user as
// its author. There is deliberately no free-text type.
type ConfigDecl struct {
	Name     string   `json:"name"`
	Type     Type     `json:"type"`
	Label    string   `json:"label,omitempty"`   // what a client shows above the field
	Example  string   `json:"example,omitempty"` // shown as a placeholder, never used as a default
	Values   []string `json:"values,omitempty"`  // the permitted values, for TypeEnum
	Optional bool     `json:"optional,omitempty"`
}

// Type is a config value's shape, and therefore what validation it must survive.
type Type string

const (
	// TypeURL is an absolute http(s) URL — a household's own server, an API base.
	TypeURL Type = "url"
	// TypeHost is a host or host:port.
	TypeHost Type = "host"
	// TypeInt is a decimal integer.
	TypeInt Type = "int"
	// TypeEnum is one of ConfigDecl.Values.
	TypeEnum Type = "enum"
)

// CredentialDecl declares a credential the host injects for an extension. It mirrors secret.Binding,
// with two additions: Host may be a config reference, because a household's own server has an address
// the publisher cannot know; and Audience says WHO the credential rides with.
//
// Header empty means the credential is NOT injected into HTTP requests — the host code reads it by
// name (an IMAP password is not a bearer). It still lives in the shard, still belongs to the owner,
// and still disappears with the extension.
type CredentialDecl struct {
	Name     string   `json:"name"`
	Host     string   `json:"host,omitempty"`   // exact host, "*.suffix", or "{{config.<name>}}"
	Header   string   `json:"header,omitempty"` // "" = not an HTTP credential
	Prefix   string   `json:"prefix,omitempty"`
	Label    string   `json:"label,omitempty"` // what a client shows when asking for the value
	Audience Audience `json:"audience,omitempty"`
}

// Audience is who a credential rides with. The zero value is the strict one, deliberately: a
// forgotten field must never widen who may spend a secret.
type Audience string

const (
	// AudienceOwner (the zero value) confines a credential to the extension's OWN requests — a
	// plugin guest's, an MCP connection's. Both carry their owner on the context.
	AudienceOwner Audience = ""
	// AudienceModel additionally lets it ride the model's own tool calls. A SKILL.md has no runtime:
	// it is text, and the model is what acts on it, so a credential confined to "the skill's own
	// calls" would be confined to a set of calls that does not exist. It is DECLARED rather than
	// derived, because the folder no longer tells you which payload a credential belongs to — and
	// deriving authority from a filename is not a rule anyone can check.
	AudienceModel Audience = "model"
)

// Validate rejects a malformed declaration fail-closed. It is called on every manifest read, so a
// declaration that could not be satisfied never reaches the injector.
func (d Decl) Validate() error {
	seen := map[string]bool{}
	for _, c := range d.Config {
		if !declRe.MatchString(c.Name) {
			return fmt.Errorf("config %q: invalid name (want %s)", c.Name, declRe)
		}
		if seen[c.Name] {
			return fmt.Errorf("config %q: declared twice", c.Name)
		}
		seen[c.Name] = true
		switch c.Type {
		case TypeURL, TypeHost, TypeInt:
		case TypeEnum:
			if len(c.Values) == 0 {
				return fmt.Errorf("config %q: enum with no values", c.Name)
			}
		default:
			return fmt.Errorf("config %q: unknown type %q", c.Name, c.Type)
		}
	}
	creds := map[string]bool{}
	for _, c := range d.Credentials {
		if !declRe.MatchString(c.Name) {
			return fmt.Errorf("credential %q: invalid name (want %s)", c.Name, declRe)
		}
		if creds[c.Name] {
			return fmt.Errorf("credential %q: declared twice", c.Name)
		}
		creds[c.Name] = true
		if c.Header == "" && c.Prefix != "" {
			return fmt.Errorf("credential %q: prefix without a header", c.Name)
		}
		if ref, ok := configRef(c.Host); ok && !seen[ref] {
			return fmt.Errorf("credential %q: host references undeclared config %q", c.Name, ref)
		}
		switch c.Audience {
		case AudienceOwner, AudienceModel:
		default:
			return fmt.Errorf("credential %q: unknown audience %q", c.Name, c.Audience)
		}
	}
	return nil
}

// Missing names the declared config values and credentials that are not supplied yet — what a client
// puts in front of a person, and what an assistant says instead of failing with a bare 401. cfg is
// the stored config, held names the credentials that have a value in the shard.
func (d Decl) Missing(cfg Values, held map[string]bool) (config []string, creds []string) {
	for _, c := range d.Config {
		if c.Optional {
			continue
		}
		if strings.TrimSpace(cfg[c.Name]) == "" {
			config = append(config, c.Name)
		}
	}
	for _, c := range d.Credentials {
		if !held[c.Name] {
			creds = append(creds, c.Name)
		}
	}
	return config, creds
}

// Values are the config values a human supplied, by declared name.
type Values map[string]string

// ValidateValues checks values against the declaration: every required name present, every value the
// shape its type promises, nothing supplied that was not declared. An undeclared name is an error
// rather than an ignored key — it is either a typo in a hand-edited file or an attempt to reach a
// substitution the manifest never offered, and neither should pass in silence.
func (d Decl) ValidateValues(v Values) error {
	declared := map[string]ConfigDecl{}
	for _, c := range d.Config {
		declared[c.Name] = c
	}
	for name := range v {
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("config %q: not declared by this extension", name)
		}
	}
	for _, c := range d.Config {
		raw, ok := v[c.Name]
		raw = strings.TrimSpace(raw)
		if !ok || raw == "" {
			if c.Optional {
				continue
			}
			return fmt.Errorf("config %q: required", c.Name)
		}
		if err := c.check(raw); err != nil {
			return fmt.Errorf("config %q: %w", c.Name, err)
		}
	}
	return nil
}

// check validates one value against its declared type. Every type refuses control characters and
// caps the length first: whatever the type is, the value ends up inside a system prompt, where a
// newline is a way to start a paragraph the author of the skill did not write.
func (c ConfigDecl) check(raw string) error {
	if len(raw) > maxValueBytes {
		return fmt.Errorf("longer than %d bytes", maxValueBytes)
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return errors.New("contains a control character")
	}
	switch c.Type {
	case TypeURL:
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("not a URL: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("scheme %q is not http or https", u.Scheme)
		}
		if u.Host == "" {
			return errors.New("no host")
		}
	case TypeHost:
		if strings.ContainsAny(raw, "/ \t?#") {
			return errors.New("not a bare host")
		}
		if _, err := url.Parse("https://" + raw); err != nil {
			return fmt.Errorf("not a host: %w", err)
		}
	case TypeInt:
		if _, err := strconv.Atoi(raw); err != nil {
			return errors.New("not an integer")
		}
	case TypeEnum:
		if !slices.Contains(c.Values, raw) {
			return fmt.Errorf("not one of %s", strings.Join(c.Values, ", "))
		}
	default:
		return fmt.Errorf("unknown type %q", c.Type)
	}
	return nil
}

// refRe matches a config reference: {{config.<name>}}, optionally with a filter — {{config.x|host}}.
var refRe = regexp.MustCompile(`\{\{\s*config\.([a-z0-9][a-z0-9_-]*)\s*(?:\|\s*(host)\s*)?\}\}`)

// configRef returns the config name a string references, if the whole string is one reference.
func configRef(s string) (string, bool) {
	m := refRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || m[0] != strings.TrimSpace(s) {
		return "", false
	}
	return m[1], true
}

// Render substitutes {{config.<name>}} references in text with the supplied values. It is how a
// text payload consumes its config — the same values an MCP server's URL or a plugin's argument
// consumes, rendered for a different reader.
//
// An unknown name is an ERROR, not an empty string: a skill body that silently loses the address of
// the server it talks about reads like a working skill and behaves like a broken one. Values are
// substituted only after ValidateValues has passed, so nothing typed can carry a newline into the
// text.
func (d Decl) Render(text string, v Values) (string, error) {
	var bad error
	out := refRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := refRe.FindStringSubmatch(m)
		name, filter := sub[1], sub[2]
		val, ok := v[name]
		if !ok || strings.TrimSpace(val) == "" {
			if bad == nil {
				bad = fmt.Errorf("unset config %q referenced in the body", name)
			}
			return m
		}
		if filter == "host" {
			return hostOf(val)
		}
		return val
	})
	return out, bad
}

// hostOf reduces a config value to the host a credential binds to: the host[:port] of a URL, or the
// value itself when it is already a bare host. The default port is dropped, because that is what an
// http.Request's URL.Host carries and the binding is compared against exactly that.
func hostOf(v string) string {
	v = strings.TrimSpace(v)
	if u, err := url.Parse(v); err == nil && u.Host != "" {
		return strings.ToLower(u.Host)
	}
	return strings.ToLower(v)
}

// Bindings turns a declaration plus its values into the host-side credential bindings for one
// extension. Only credentials with a Header produce a binding — the others live in the shard for the
// host code to read by name. A binding whose host references an unset config value is DROPPED rather
// than registered against the empty host: hostMatches fails closed on "", but a binding that cannot
// name its destination is a configuration error, and the caller reports it as one.
func (d Decl) Bindings(owner string, v Values) ([]secret.Binding, error) {
	var out []secret.Binding
	for _, c := range d.Credentials {
		if c.Header == "" {
			continue
		}
		host, err := d.resolveHost(c, v)
		if err != nil {
			return nil, err
		}
		out = append(out, secret.Binding{
			Secret:  SecretName(owner, host, c.Name),
			Host:    host,
			Header:  c.Header,
			Prefix:  c.Prefix,
			Ambient: c.Audience == AudienceModel,
		})
	}
	return out, nil
}

// Keys are the vault keys this extension's credentials live under, by credential name — what a
// `secret set` resolves and what a "which credentials are missing" check looks up.
func (d Decl) Keys(owner string, v Values) (map[string]string, error) {
	out := make(map[string]string, len(d.Credentials))
	for _, c := range d.Credentials {
		host, err := d.resolveHost(c, v)
		if err != nil {
			return nil, err
		}
		out[c.Name] = SecretName(owner, host, c.Name)
	}
	return out, nil
}

// ResolveHostFor is resolveHost for one credential, exported so a caller that is about to DELETE an
// extension can learn which hosts its credentials were bound to while the declaration still exists.
func (d Decl) ResolveHostFor(c CredentialDecl, v Values) (string, error) { return d.resolveHost(c, v) }

// resolveHost turns a declared host into the concrete one, following a config reference.
// Lowercased at this one point, because the two consumers disagree otherwise: SecretName lowercases
// the host into the key, while hostMatches compares the binding's host EXACTLY. A manifest declaring
// "API.Example.com" would then store its token under one name and match requests under another — the
// credential is seedable, listable, and never injected, which surfaces as a 401 with no diagnostic.
func (d Decl) resolveHost(c CredentialDecl, v Values) (string, error) {
	ref, isRef := configRef(c.Host)
	if !isRef {
		return strings.ToLower(c.Host), nil
	}
	val := strings.TrimSpace(v[ref])
	if val == "" {
		return "", fmt.Errorf("credential %q: config %q is not set, so its host is unknown", c.Name, ref)
	}
	return hostOf(val), nil
}

// ParseDecl reads a declaration in its serialized form — what an install has been handed and is about
// to write, or what a client is about to render a form from. LoadDecl is the same check one step
// later, against what is already on disk.
//
// It returns the declaration rather than only a verdict, so that a caller wanting the config fields
// does not unmarshal the same bytes a second time: a second reader is a second set of rules, and the
// one without the size cap and Validate is the one that gets used for display.
func ParseDecl(data []byte) (Decl, error) {
	if len(data) > maxManifestBytes {
		return Decl{}, fmt.Errorf("%s: larger than %d bytes", ManifestFile, maxManifestBytes)
	}
	var d Decl
	if err := json.Unmarshal(data, &d); err != nil {
		return Decl{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if err := d.Validate(); err != nil {
		return Decl{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	return d, nil
}

// LoadDecl reads and validates an extension folder's manifest. A folder with no manifest is not an
// error and yields the zero Decl: a skill that needs nothing declares nothing, which is why the file
// is optional and its absence means "no config, no credentials" rather than "broken".
func LoadDecl(dir string) (Decl, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return Decl{}, nil
	}
	if err != nil {
		return Decl{}, err
	}
	return ParseDecl(data)
}

// LoadValues reads an extension folder's config.json and CHECKS it against the declaration. A missing
// file yields empty values — an extension that was installed but not configured yet, which is a state
// a client has to render rather than an error to fail on.
//
// Validating on the way IN, not only in SaveValues, is what makes the typing worth anything: these
// values are substituted into text the model reads on every turn, and a file somebody edited by hand
// (or an older one whose declaration has since changed) must not be able to carry a newline into a
// system prompt just because it did not come through the writer.
func LoadValues(dir string, d Decl) (Values, error) {
	data, err := os.ReadFile(filepath.Join(dir, ConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return Values{}, nil
	}
	if err != nil {
		return nil, err
	}
	var v Values
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	if v == nil {
		v = Values{}
	}
	// A partially filled config is not an error — that is the unconfigured state. A value that is
	// PRESENT and malformed is: it would be substituted verbatim.
	for name, raw := range v {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		decl, ok := d.config(name)
		if !ok {
			return nil, fmt.Errorf("%s: %q is not declared by this extension", ConfigFile, name)
		}
		if err := decl.check(strings.TrimSpace(raw)); err != nil {
			return nil, fmt.Errorf("%s: %q: %w", ConfigFile, name, err)
		}
	}
	return v, nil
}

// config finds one declared setting by name.
func (d Decl) config(name string) (ConfigDecl, bool) {
	for _, c := range d.Config {
		if c.Name == name {
			return c, true
		}
	}
	return ConfigDecl{}, false
}

// SaveValues validates values against the declaration and writes them to the extension folder. It
// writes through a temp file in the same directory, so a crash mid-write leaves the old config
// intact rather than a half-written one that fails to parse on the next start.
func SaveValues(dir string, d Decl, v Values) error {
	if err := d.ValidateValues(v); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ConfigFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, ConfigFile))
}
