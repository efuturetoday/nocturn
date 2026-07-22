package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
)

// A server declaration lives in the workspace CONTROL-PLANE as one file per
// server (<ws>/mcp/<name>.json — host-managed, a sibling of the model's mnt/
// mount) because it is authority-relevant (ADR-10): declaring a server grants
// the model that server's tools and wires YOUR token to its host. The model can
// neither read nor write it; presence IS the authorization (admin-authored),
// like the plugins/ directory. One file per server is the portable/purgeable
// unit: dropping <name>.json removes exactly that server (mirroring a plugin or
// agent folder).

// Server is one declared remote MCP server.
//
// Name is NOT a config field: it comes from the file's basename (<name>.json),
// the single source of identity — so it can never drift from the filename, and a
// stray "name" key in the JSON is rejected (DisallowUnknownFields).
//
// Auth selects how the connection's Bearer is obtained; it never carries a
// secret value. "token": the operator seeds the Bearer into the encrypted vault
// out of band under the connection's owner-namespaced secret
// ("mcp:<server>@<host>/oauth"), and it is injected host-side — nothing secret
// ever touches the file or the environment. "" with an OAuth block runs the
// OAuth flow instead; "" with no block means no credential (a public server).
// "token" and an OAuth block are mutually exclusive (one credential, one source).
type Server struct {
	Name  string     `json:"-"` // from the filename, not the file
	URL   string     `json:"url"`
	Auth  string     `json:"auth,omitempty"`
	OAuth *OAuthDecl `json:"oauth,omitempty"`
}

// OAuthDecl mirrors plugin.OAuthDecl: config-supplied endpoints + client id —
// the host runs the authorization-code (+PKCE) flow, holds and refreshes the
// token, and injects it as the connection's Bearer. The server never defines
// our OAuth client; discovery/DCR is a deliberate non-feature for now (see
// FRAGEN.md).
type OAuthDecl struct {
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
}

// nameRe constrains a server name: it prefixes every exposed tool
// ("<server>_<tool>") and the credential owner ("mcp:<server>"). The exposed
// tool name must satisfy OpenAI/agentkit's ^[a-zA-Z0-9_-]{1,64}$ (a dot is
// rejected with HTTP 400), so the server name forbids dots and is length-capped
// to leave room for the "_<tool>" suffix inside 64 chars.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Validate rejects a malformed server declaration fail-closed: an odd name, a
// non-https URL (a token must never ride to a cleartext endpoint), an unknown
// auth mode, a "token" auth alongside an OAuth block (one credential, one
// source — ambiguity is not resolved silently), or an OAuth block missing its
// client_id/scopes or using non-https endpoints.
func (s Server) Validate() error {
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("mcpcap: invalid server name %q (want ^[a-z0-9][a-z0-9_-]{0,31}$)", s.Name)
	}
	if !isHTTPSURL(s.URL) {
		return fmt.Errorf("mcpcap: server %q: url must be a https URL (got %q)", s.Name, s.URL)
	}
	if s.Auth != "" && s.Auth != "token" {
		return fmt.Errorf("mcpcap: server %q: unknown auth mode %q (want \"token\" or omit)", s.Name, s.Auth)
	}
	if s.Auth == "token" && s.OAuth != nil {
		return fmt.Errorf("mcpcap: server %q: auth \"token\" and oauth are mutually exclusive", s.Name)
	}
	if o := s.OAuth; o != nil {
		if o.ClientID == "" || len(o.Scopes) == 0 {
			return fmt.Errorf("mcpcap: server %q: oauth needs a client_id and at least one scope", s.Name)
		}
		if !isHTTPSURL(o.AuthURL) || !isHTTPSURL(o.TokenURL) {
			return fmt.Errorf("mcpcap: server %q: oauth auth_url and token_url must be https URLs", s.Name)
		}
	}
	return nil
}

// isHTTPSURL reports whether s is a well-formed https:// URL with a host.
func isHTTPSURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// Discover reads every <dir>/<name>.json server declaration into a Set WITHOUT
// connecting to any of them. A missing dir yields an empty Set. A malformed file
// (unknown fields including a stray "name", an invalid entry) is SKIPPED with an
// Error diagnostic rather than failing the whole scan — its tools and token
// wiring are then simply absent (fail-closed), and the other servers still load.
// The server's name IS the filename stem — the single source of identity.
func Discover(dir string, diag *agentkit.Diagnostics) Set {
	set := Set{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return set
	}
	if err != nil {
		diagnose(diag, "mcp", fmt.Sprintf("read dir %s: %v", dir, err))
		return set
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue // only <name>.json files are server declarations
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		srv, err := loadServer(filepath.Join(dir, e.Name()), name)
		if err != nil {
			diagnose(diag, "mcp:"+name, err.Error())
			continue
		}
		set[srv.Name] = srv
	}
	return set
}

// loadServer reads and validates one <name>.json server declaration. The name is
// the filename stem, never a field — Validate rejects a stem that is not a valid
// server name.
func loadServer(path, name string) (Server, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Server{}, fmt.Errorf("read: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Server
	if err := dec.Decode(&s); err != nil {
		return Server{}, fmt.Errorf("parse: %w", err)
	}
	s.Name = name
	if err := s.Validate(); err != nil {
		return Server{}, err
	}
	return s, nil
}

// diagnose feeds one discovery finding into the collector if present (nil-safe:
// the OAuth aggregator discovers without a collector).
func diagnose(diag *agentkit.Diagnostics, subject, msg string) {
	if diag != nil {
		diag.Error(subject, msg)
	}
}
