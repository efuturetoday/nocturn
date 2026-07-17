package oauth_test

import (
	"context"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/oauth"
)

func TestProvider_CarriesEndpointsAndScopes(t *testing.T) {
	cfg := oauth.Provider("https://auth.example/authorize", "https://auth.example/token", "cid", "secret", "scope.a", "scope.b")
	if cfg.ClientID != "cid" || cfg.ClientSecret != "secret" {
		t.Fatalf("client id/secret = %q/%q", cfg.ClientID, cfg.ClientSecret)
	}
	if len(cfg.Scopes) != 2 || cfg.Scopes[0] != "scope.a" {
		t.Fatalf("scopes = %v, want the two given", cfg.Scopes)
	}
	if cfg.Endpoint.AuthURL != "https://auth.example/authorize" || cfg.Endpoint.TokenURL != "https://auth.example/token" {
		t.Fatalf("endpoint not carried through: %+v", cfg.Endpoint)
	}
}

// Authorize builds a consent URL carrying PKCE (S256), offline access + consent
// (needed for a refresh token), a state param, and the requested scope; and it
// binds the redirect to loopback. Driven with an already-cancelled context so it
// returns immediately after building the URL — no browser, no real callback.
func TestAuthorize_ConsentURLAndLoopback(t *testing.T) {
	cfg := oauth.Provider("https://auth.example/authorize", "https://auth.example/token", "client-id", "", "read.scope")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ceremony returns via ctx.Done() right after prompting

	var gotURL string
	if _, err := oauth.Authorize(ctx, cfg, func(u string) { gotURL = u }); err == nil {
		t.Fatal("expected a timeout/cancel error with a cancelled context")
	}
	for _, want := range []string{
		"response_type=code", "code_challenge_method=S256", "code_challenge=",
		"access_type=offline", "prompt=consent", "state=", "read.scope",
	} {
		if !strings.Contains(gotURL, want) {
			t.Fatalf("consent URL missing %q:\n%s", want, gotURL)
		}
	}
	if !strings.Contains(cfg.RedirectURL, "127.0.0.1") {
		t.Fatalf("redirect must be loopback, got %q", cfg.RedirectURL)
	}
}
