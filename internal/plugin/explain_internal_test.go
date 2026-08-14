package plugin

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/secret"
)

// A plugin whose account was never connected fails at the credential boundary, and the message it
// produces is the whole of what the person sees — so it has to name the command, not the vault key.
func TestExplain_NamesTheAuthCommand(t *testing.T) {
	p := &Plugin{manifest: Manifest{
		Name:        "gmail",
		Credentials: []CredentialDecl{{Name: "account", Host: "gmail.googleapis.com", Header: "Authorization"}},
		OAuth:       []OAuthDecl{{Name: "account"}},
	}}

	got := p.explain(fmt.Errorf("credential %q: %w", "plugin:gmail/account", secret.ErrNotFound))
	if !strings.Contains(got.Error(), "nocturn auth gmail") {
		t.Errorf("explain() = %v, want it to name the plugin, which is what `nocturn auth` matches", got)
	}
	if !errors.Is(got, secret.ErrNotFound) {
		t.Error("explain() dropped the wrapped error; a caller can no longer tell why it failed")
	}
}

// A plugin with a credential but no OAuth is seeded by hand, so the sentence has to point at the
// other command — and at the owner-namespaced name, which is the part nobody guesses.
func TestExplain_NamesTheSecretForAHandSeededCredential(t *testing.T) {
	p := &Plugin{manifest: Manifest{
		Name:        "tracker",
		Credentials: []CredentialDecl{{Name: "token", Host: "api.example.com", Header: "Authorization"}},
	}}

	got := p.explain(fmt.Errorf("credential: %w", secret.ErrNotFound)).Error()
	if !strings.Contains(got, "nocturn secret set plugin:tracker/token") {
		t.Errorf("explain() = %q, want it to name the secret to seed", got)
	}
}

// Anything else passes through untouched: a denied action or a 404 must not be dressed up as a
// missing login, which would send somebody to re-authorize an account that is already connected.
func TestExplain_LeavesUnrelatedErrorsAlone(t *testing.T) {
	p := &Plugin{manifest: Manifest{Name: "gmail", OAuth: []OAuthDecl{{Name: "account"}}}}
	other := errors.New("denied by the human")

	if got := p.explain(other); got != other {
		t.Errorf("explain() = %v, want the error unchanged", got)
	}
}
