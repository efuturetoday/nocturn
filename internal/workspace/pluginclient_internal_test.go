package workspace

import (
	"encoding/json"
	"testing"

	"github.com/efuturetoday/nocturn/internal/plugin"
)

// A catalog plugin for a provider with restricted scopes ships no OAuth client — a shared one would
// need an annual third-party security assessment, and every household's mail would then run through
// one project. The person supplies theirs once with `nocturn auth <plugin> --client-id …`, which
// stores it in the plugin's shard, and the daemon has to refresh with THAT. Refreshing with the
// manifest's would fail an hour after connecting, which is the worst moment to find out.
func TestPluginRecord_ClientPrecedence(t *testing.T) {
	manifest := plugin.OAuthProvider{
		Name:       "account",
		SecretName: "plugin:gmail/account",
		AuthURL:    "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:   "https://oauth2.googleapis.com/token",
		Scopes:     []string{"https://www.googleapis.com/auth/gmail.readonly"},
	}

	for name, tc := range map[string]struct {
		provider           plugin.OAuthProvider
		stored             *OAuthRecord
		wantID, wantSecret string
	}{
		"nothing stored, manifest carries a client": {
			provider: withClient(manifest, "from-manifest", "s1"),
			wantID:   "from-manifest", wantSecret: "s1",
		},
		"stored client wins": {
			provider: withClient(manifest, "from-manifest", "s1"),
			stored:   &OAuthRecord{ClientID: "from-the-shard", ClientSecret: "s2"},
			wantID:   "from-the-shard", wantSecret: "s2",
		},
		"stored client supplies what the manifest omits": {
			provider: manifest,
			stored:   &OAuthRecord{ClientID: "from-the-shard"},
			wantID:   "from-the-shard",
		},
		// Only the client moves. A stored record naming other endpoints must not redirect where the
		// authorization goes: the manifest is signed and the shard is not.
		"a stored record cannot move the endpoints": {
			provider: withClient(manifest, "from-manifest", ""),
			stored:   &OAuthRecord{ClientID: "c", AuthURL: "https://evil.example/a", TokenURL: "https://evil.example/t"},
			wantID:   "c",
		},
		// A half-written record must not blank out a working client.
		"a stored record without a client changes nothing": {
			provider: withClient(manifest, "from-manifest", "s1"),
			stored:   &OAuthRecord{Scopes: []string{"read"}},
			wantID:   "from-manifest", wantSecret: "s1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			tokens := fakeTokens{}
			if tc.stored != nil {
				if err := StoreOAuthRecord(tokens, tc.provider.SecretName, *tc.stored); err != nil {
					t.Fatal(err)
				}
			}
			got := pluginRecord(tc.provider, tokens)

			if got.ClientID != tc.wantID || got.ClientSecret != tc.wantSecret {
				t.Errorf("client = %q/%q, want %q/%q", got.ClientID, got.ClientSecret, tc.wantID, tc.wantSecret)
			}
			if got.AuthURL != manifest.AuthURL || got.TokenURL != manifest.TokenURL {
				t.Errorf("endpoints = %q/%q, want the manifest's %q/%q",
					got.AuthURL, got.TokenURL, manifest.AuthURL, manifest.TokenURL)
			}
		})
	}
}

// A stored record that will not decode is not a record: the manifest's client has to survive it
// rather than the provider ending up half-configured.
func TestPluginRecord_IgnoresAnUnreadableRecord(t *testing.T) {
	p := plugin.OAuthProvider{
		SecretName: "plugin:x/acct",
		AuthURL:    "https://a.example/x",
		TokenURL:   "https://t.example/y",
		ClientID:   "from-manifest",
	}
	tokens := fakeTokens{p.SecretName + providerSuffix: json.RawMessage(`not json`)}

	if got := pluginRecord(p, tokens); got.ClientID != "from-manifest" {
		t.Errorf("client = %q, want the manifest's to survive an unreadable record", got.ClientID)
	}
}

func withClient(p plugin.OAuthProvider, id, secret string) plugin.OAuthProvider {
	p.ClientID, p.ClientSecret = id, secret
	return p
}

// fakeTokens is an in-memory TokenStore. The shard routing has its own tests; what matters here is
// which client the wiring picks.
type fakeTokens map[string][]byte

func (f fakeTokens) Get(name string) ([]byte, bool) { v, ok := f[name]; return v, ok }

func (f fakeTokens) Set(name string, v []byte) error {
	f[name] = v
	return nil
}
