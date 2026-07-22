package mcp

import (
	"net/url"
	"os"
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

// DiscoverOAuth scans <root>/<ws>/mcp.json in every workspace and returns the OAuth-declaring
// servers as OAuthProviders. A missing or invalid config yields none (LoadConfig already validated
// the servers on the startup path; here we only collect providers, never connect).
//
// The vault key is SecretName(srv.Name, u.Host) — the host carries the port, exactly like NewConn's
// binding — so a token authorized here injects into that connection and nowhere else. A plugin and an
// MCP server may share a provider name (both answer `nocturn auth <name>`); their keys still differ
// (the MCP key is owner-namespaced and host-bound), so no credential can cross.
func DiscoverOAuth(root string) []OAuthProvider {
	var out []OAuthProvider
	spaces, _ := os.ReadDir(root)
	for _, ws := range spaces {
		if !ws.IsDir() {
			continue
		}
		servers, err := LoadConfig(filepath.Join(root, ws.Name(), "mcp.json"))
		if err != nil {
			continue // a broken config is surfaced on the real startup path, not here
		}
		for _, srv := range servers {
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
	}
	return out
}
