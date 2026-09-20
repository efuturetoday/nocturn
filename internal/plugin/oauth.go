package plugin

import (
	"os"
	"path/filepath"

	"github.com/efuturetoday/nocturn/internal/extension"
)

// OAuthProvider is one plugin OAuth declaration flattened with the vault key its token lives under —
// the owner-namespaced SecretName, the SAME key installPlugins binds the credential's secret to, so an
// authorized token flows straight into that credential's injection. It is the neutral hand-off to the
// daemon's OAuth wiring, so plugin-manifest knowledge stays in this package.
type OAuthProvider struct {
	Name         string // provider id for `nocturn auth <name>` (links to a CredentialDecl.Name)
	SecretName   string // the owner+host-bound vault key the workspace binds this credential to
	AuthURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// DiscoverOAuth scans one workspace's <wsDir>/plugins for declared OAuth providers. A plugin that
// fails to load is skipped (its own load path reports it on the real startup path); this is only
// about collecting providers, never running them. The caller (per-workspace secret assembly) owns
// scoping to a single workspace, so credentials never leak across workspace vaults.
func DiscoverOAuth(wsDir string) []OAuthProvider {
	var out []OAuthProvider
	entries, _ := os.ReadDir(filepath.Join(wsDir, extension.Dir))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		loaded, err := Load(filepath.Join(wsDir, extension.Dir, e.Name()))
		if err != nil {
			continue
		}
		keys, err := SecretNames(loaded.Manifest)
		if err != nil {
			continue
		}
		for _, o := range loaded.Manifest.OAuth {
			out = append(out, OAuthProvider{
				Name:         o.Name,
				SecretName:   keys[o.Name],
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
