package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/internal/oauth"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// testVaultKey is a fixed 32-byte AES key (the vault takes a key, not a passphrase).
var testVaultKey = bytes.Repeat([]byte{0x7C}, 32)

func openTestVault(t *testing.T, path string) *secret.Vault {
	t.Helper()
	v, err := secret.OpenVault(path, testVaultKey)
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	return v
}

// The full OAuth-in-the-vault loop: a token saved into the vault seeds an
// oauth.Credential; a refresh (expired token → fake token endpoint, never a
// real OAuth server) fires the persistToken hook, which writes the fresh token
// back into the vault — visible after a full reopen from the encrypted file,
// with the refresh token preserved.
func TestVaultToken_RefreshWritesBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh","token_type":"Bearer","refresh_token":"r-2","expires_in":3600}`))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "secrets.age")
	vault := openTestVault(t, path)

	expired := &oauth2.Token{AccessToken: "old", RefreshToken: "r-1", Expiry: time.Now().Add(-time.Hour)}
	if err := saveVaultToken(vault, "google", expired); err != nil {
		t.Fatalf("saveVaultToken: %v", err)
	}

	// Seed the credential from the vault, exactly like wireGoogleCredential.
	tok, ok := vaultToken(vault, "google")
	if !ok || tok.AccessToken != "old" || tok.RefreshToken != "r-1" {
		t.Fatalf("vaultToken = %+v, %v", tok, ok)
	}
	cfg := &oauth2.Config{ClientID: "id", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}}
	cred := oauth.NewCredential(cfg, tok, persistToken(vault, "google"))

	v, err := cred.Value(context.Background())
	if err != nil || string(v) != "fresh" {
		t.Fatalf("Value = %q, %v", v, err)
	}

	// The refreshed token (rotated refresh token included) survived a reopen of
	// the encrypted vault file.
	re := openTestVault(t, path)
	saved, ok := vaultToken(re, "google")
	if !ok || saved.AccessToken != "fresh" || saved.RefreshToken != "r-2" {
		t.Fatalf("reloaded token = %+v, %v — refresh did not write back to the vault", saved, ok)
	}
}

// A stored blob without a refresh token is treated as absent (we could not
// refresh it), forcing a clean re-auth instead of a doomed credential.
func TestVaultToken_NoRefreshToken_Absent(t *testing.T) {
	vault := openTestVault(t, filepath.Join(t.TempDir(), "secrets.age"))
	data, _ := json.Marshal(&oauth2.Token{AccessToken: "a"})
	if err := vault.Set("google", data); err != nil {
		t.Fatal(err)
	}
	if _, ok := vaultToken(vault, "google"); ok {
		t.Fatal("a token without a refresh token must read as absent")
	}
}
