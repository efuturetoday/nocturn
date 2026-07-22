package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"regexp"
)

// The server config (mcp.json) lives in the workspace CONTROL-PLANE
// (<ws>/mcp.json — host-managed, a sibling of the model's mnt/ mount) because it
// is authority-relevant (ADR-10): declaring a server grants the model that
// server's tools and wires YOUR token to its host. The model can neither read
// nor write it; presence in mcp.json IS the authorization (admin-authored), like
// the plugins/ directory.

// Server is one declared remote MCP server.
//
// Auth selects how the connection's Bearer is obtained; it never carries a
// secret value. "token": the operator seeds the Bearer into the encrypted vault
// out of band under the connection's owner-namespaced secret
// ("mcp:<server>@<host>/oauth"), and it is injected host-side — nothing secret
// ever touches mcp.json or the environment. "" with an OAuth block runs the
// OAuth flow instead; "" with no block means no credential (a public server).
// "token" and an OAuth block are mutually exclusive (one credential, one source).
type Server struct {
	Name  string     `json:"name"`
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

// LoadConfig reads the declared servers from path WITHOUT connecting to any of
// them. A missing file means no servers (nil, nil). Unknown fields, invalid
// entries, and duplicate names are rejected fail-closed: the workspace never
// half-loads a config the code only partly understood.
func LoadConfig(path string) ([]Server, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mcpcap: read config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg struct {
		Servers []Server `json:"servers"`
	}
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("mcpcap: parse %s: %w", path, err)
	}
	seen := map[string]bool{}
	for _, s := range cfg.Servers {
		if err := s.Validate(); err != nil {
			return nil, err
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("mcpcap: duplicate server %q", s.Name)
		}
		seen[s.Name] = true
	}
	return cfg.Servers, nil
}
