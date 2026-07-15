package oauth_test

import (
	"context"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/oauth"
)

func TestGoogle_DefaultsToGmailScope(t *testing.T) {
	cfg := oauth.Google("cid", "secret")
	if cfg.ClientID != "cid" || cfg.ClientSecret != "secret" {
		t.Fatalf("client id/secret = %q/%q", cfg.ClientID, cfg.ClientSecret)
	}
	if len(cfg.Scopes) != 1 || cfg.Scopes[0] != oauth.GmailReadonlyScope {
		t.Fatalf("scopes = %v, want [gmail.readonly]", cfg.Scopes)
	}
	if cfg.Endpoint.AuthURL == "" || cfg.Endpoint.TokenURL == "" {
		t.Fatalf("Google endpoint not set: %+v", cfg.Endpoint)
	}
}

func TestGoogle_HonorsExplicitScopes(t *testing.T) {
	cfg := oauth.Google("cid", "", "scope.a", "scope.b")
	if len(cfg.Scopes) != 2 || cfg.Scopes[0] != "scope.a" {
		t.Fatalf("scopes = %v, want the two given", cfg.Scopes)
	}
}

// Authorize builds a consent URL carrying PKCE (S256), offline access + consent
// (needed for a refresh token), a state param, and the requested scope; and it
// binds the redirect to loopback. Driven with an already-cancelled context so it
// returns immediately after building the URL — no browser, no real callback.
func TestAuthorize_ConsentURLAndLoopback(t *testing.T) {
	cfg := oauth.Google("client-id", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ceremony returns via ctx.Done() right after prompting

	var gotURL string
	if _, err := oauth.Authorize(ctx, cfg, func(u string) { gotURL = u }); err == nil {
		t.Fatal("expected a timeout/cancel error with a cancelled context")
	}
	for _, want := range []string{
		"response_type=code", "code_challenge_method=S256", "code_challenge=",
		"access_type=offline", "prompt=consent", "state=", "gmail.readonly",
	} {
		if !strings.Contains(gotURL, want) {
			t.Fatalf("consent URL missing %q:\n%s", want, gotURL)
		}
	}
	if !strings.Contains(cfg.RedirectURL, "127.0.0.1") {
		t.Fatalf("redirect must be loopback, got %q", cfg.RedirectURL)
	}
}
