package oauth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/internal/oauth"
)

// An expired token is transparently refreshed: Value returns the fresh access
// token, hits the token endpoint once, and fires onChange for persistence.
func TestSource_RefreshesAndPersists(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	cfg := &oauth2.Config{ClientID: "id", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}}
	expired := &oauth2.Token{AccessToken: "old", RefreshToken: "r", Expiry: time.Now().Add(-time.Hour)}

	var saved *oauth2.Token
	src := oauth.NewSource(cfg, expired, func(tok *oauth2.Token) { saved = tok })

	v, err := src.Value(context.Background())
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if string(v) != "fresh" {
		t.Fatalf("access token = %q, want fresh", v)
	}
	if hits != 1 {
		t.Fatalf("token endpoint hit %d times, want 1", hits)
	}
	if saved == nil || saved.AccessToken != "fresh" {
		t.Fatalf("onChange not fired with fresh token: %+v", saved)
	}
}

// A still-valid token is reused: no refresh, no HTTP, no onChange.
func TestSource_CachesValid(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	cfg := &oauth2.Config{ClientID: "id", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}}
	valid := &oauth2.Token{AccessToken: "good", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)}

	fired := false
	src := oauth.NewSource(cfg, valid, func(*oauth2.Token) { fired = true })
	v, err := src.Value(context.Background())
	if err != nil || string(v) != "good" {
		t.Fatalf("v=%q err=%v, want good/nil", v, err)
	}
	if hits != 0 || fired {
		t.Fatalf("valid token must not refresh (hits=%d fired=%v)", hits, fired)
	}
}

// Concurrent Value calls on an expired token refresh exactly once (single-flight
// by the underlying TokenSource) and see no data race. Run with -race.
func TestSource_Concurrent(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	cfg := &oauth2.Config{ClientID: "id", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}}
	expired := &oauth2.Token{AccessToken: "old", RefreshToken: "r", Expiry: time.Now().Add(-time.Hour)}
	src := oauth.NewSource(cfg, expired, nil)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := src.Value(context.Background())
			if err != nil {
				errs <- err
			} else if string(v) != "fresh" {
				errs <- context.DeadlineExceeded // placeholder non-nil
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Value: %v", err)
	}
	if hits != 1 {
		t.Fatalf("token endpoint hit %d times, want exactly 1 (single-flight)", hits)
	}
}

// A refresh that fails is fail-closed: Value returns an error, no token.
func TestSource_RefreshError_FailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	cfg := &oauth2.Config{ClientID: "id", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}}
	expired := &oauth2.Token{AccessToken: "old", RefreshToken: "r", Expiry: time.Now().Add(-time.Hour)}
	src := oauth.NewSource(cfg, expired, nil)

	if _, err := src.Value(context.Background()); err == nil {
		t.Fatal("expected an error on refresh failure, got nil")
	}
}
