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

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/discovery"
)

// A server declaration lives in the workspace CONTROL-PLANE as one FOLDER per
// server (<ws>/mcp/<name>/mcp.json — host-managed, a sibling of the model's mnt/
// mount) because it is authority-relevant (ADR-10): declaring a server grants
// the model that server's tools and wires YOUR token to its host. The model can
// neither read nor write it; presence IS the authorization (admin-authored),
// like the plugins/ directory. One folder per server is the portable/purgeable
// unit and gives the server a home for its secret shard (secrets.enc): dropping
// <ws>/mcp/<name>/ removes exactly that server (mirroring a plugin or agent folder).

// Server is one declared remote MCP server.
//
// Name defaults to the server's folder name; a "name" field may override it (the
// shared discovery rule — see discovery.ResolveName), and a name that disagrees
// with the folder is surfaced as a warning. The name namespaces the server's tools
// ("<server>_<tool>") and its credential owner ("mcp:<server>").
//
// Auth selects how the connection's Bearer is obtained; it never carries a
// secret value. "token": the operator seeds the Bearer into the encrypted vault
// out of band under the connection's owner-namespaced secret
// ("mcp:<server>@<host>/oauth"), and it is injected host-side — nothing secret
// ever touches the file or the environment. "" with an OAuth block runs the
// OAuth flow instead; "" with no block means no credential (a public server).
// "token" and an OAuth block are mutually exclusive (one credential, one source).
type Server struct {
	Name  string     `json:"name,omitempty"` // defaults to the folder name; see discovery.ResolveName
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

// Discover reads every <dir>/<name>/mcp.json server declaration into a Set WITHOUT
// connecting to any of them. Each server is a FOLDER (like a plugin), so its secret
// shard (secrets.enc) can live beside its manifest. A missing dir yields an empty
// Set. A subfolder without an mcp.json is skipped (it may hold only a shard). A
// malformed manifest is SKIPPED with a diagnostic rather than failing the whole
// scan — its tools and token wiring are then simply absent (fail-closed), and the
// other servers still load. The server's name defaults to the FOLDER name; a "name"
// field may override it (discovery.ResolveName). A duplicate name keeps the first.
func Discover(dir string, diag *agentkit.Diagnostics) Set {
	set := Set{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return set
	}
	if err != nil {
		discovery.Diagnose(diag, "mcp", fmt.Sprintf("read dir %s: %v", dir, err))
		return set
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue // each server is a folder
		}
		manifest := filepath.Join(dir, e.Name(), "mcp.json")
		if info, err := os.Stat(manifest); err != nil || info.IsDir() {
			continue // a folder without an mcp.json is not a server
		}
		srv, err := loadServer(manifest)
		if err != nil {
			discovery.Diagnose(diag, "mcp:"+e.Name(), err.Error())
			continue
		}
		srv.Name = discovery.ResolveName(diag, "mcp", e.Name(), srv.Name)
		if err := srv.Validate(); err != nil {
			discovery.Diagnose(diag, "mcp:"+srv.Name, err.Error())
			continue
		}
		if _, dup := set[srv.Name]; dup {
			discovery.Diagnose(diag, "mcp:"+srv.Name, "skipped (duplicate name; first wins)")
			continue
		}
		set[srv.Name] = srv
	}
	return set
}

// loadServer reads and decodes one mcp.json server declaration (name resolved
// and validated by Discover).
func loadServer(path string) (Server, error) {
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
	return s, nil
}
