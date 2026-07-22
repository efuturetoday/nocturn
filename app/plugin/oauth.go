package plugin

import (
	"os"
	"path/filepath"
)

// OAuthProvider is one plugin OAuth declaration flattened with the vault key its token lives under —
// the owner-namespaced SecretName, the SAME key installPlugins binds the credential's secret to, so an
// authorized token flows straight into that credential's injection. It is the neutral hand-off to the
// daemon's OAuth wiring, so plugin-manifest knowledge stays in this package.
type OAuthProvider struct {
	Name         string // provider id for `nocturn auth <name>` (links to a CredentialDecl.Name)
	SecretName   string // SecretName(plugin, cred) — the owner-namespaced vault key installPlugins binds to
	AuthURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// DiscoverOAuth scans <root>/<ws>/plugins in every workspace and returns the plugins' declared OAuth
// providers. A plugin that fails to load is skipped (its own load path reports it on the real startup
// path); this is only about collecting providers, never running them.
func DiscoverOAuth(root string) []OAuthProvider {
	var out []OAuthProvider
	spaces, _ := os.ReadDir(root)
	for _, ws := range spaces {
		if !ws.IsDir() {
			continue
		}
		pluginsDir := filepath.Join(root, ws.Name(), "plugins")
		entries, _ := os.ReadDir(pluginsDir)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			loaded, err := Load(filepath.Join(pluginsDir, e.Name()))
			if err != nil {
				continue
			}
			for _, o := range loaded.Manifest.OAuth {
				out = append(out, OAuthProvider{
					Name:         o.Name,
					SecretName:   SecretName(loaded.Manifest.Name, o.Name),
					AuthURL:      o.AuthURL,
					TokenURL:     o.TokenURL,
					ClientID:     o.ClientID,
					ClientSecret: o.ClientSecret,
					Scopes:       o.Scopes,
				})
			}
		}
	}
	return out
}
