package mcp

import (
	"net/url"
	"path/filepath"
)

// OAuthProvider is one MCP server's OAuth declaration flattened with the vault key its bearer lives
// under (host-bound, the SAME key NewConn's binding names). It is the neutral hand-off to the
// daemon's OAuth wiring: mcp.json parsing and key derivation stay in this package, so the OAuth
// aggregator never learns MCP's config shape.
type OAuthProvider struct {
	Name         string // server name = provider id for `nocturn auth <name>`
	SecretName   string // SecretName(name, u.Host) — the host-bound vault key NewConn binds to
	AuthURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// DiscoverOAuth reads one workspace's <wsDir>/mcp/*.json and returns the OAuth-declaring servers as
// OAuthProviders. A missing or invalid config yields none (Discover already surfaced any bad file on
// the startup path via diagnostics; here we only collect providers, never connect). The caller
// (per-workspace secret assembly) owns scoping to a single workspace, so credentials never leak.
//
// The vault key is SecretName(srv.Name, u.Host) — the host carries the port, exactly like NewConn's
// binding — so a token authorized here injects into that connection and nowhere else. A plugin and an
// MCP server may share a provider name (both answer `nocturn auth <name>`); their keys still differ
// (the MCP key is owner-namespaced and host-bound), so no credential can cross.
func DiscoverOAuth(wsDir string) []OAuthProvider {
	var out []OAuthProvider
	servers := Discover(filepath.Join(wsDir, "mcp"), nil) // a broken file is surfaced on the real startup path
	for _, srv := range servers.All() {
		if srv.OAuth == nil {
			continue
		}
		u, err := url.Parse(srv.URL)
		if err != nil {
			continue
		}
		o := srv.OAuth
		out = append(out, OAuthProvider{
			Name:         srv.Name,
			SecretName:   SecretName(srv.Name, u.Host),
			AuthURL:      o.AuthURL,
			TokenURL:     o.TokenURL,
			ClientID:     o.ClientID,
			ClientSecret: o.ClientSecret,
			Scopes:       o.Scopes,
		})
	}
	return out
}
