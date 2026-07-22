package oauth_test

import (
	"context"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/app/secret/oauth"
)

func TestSource_ValueReturnsAccessTokenBytes(t *testing.T) {
	ts := newTokenServer(t)
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	initial := &oauth2.Token{
		AccessToken:  "initial-access",
		RefreshToken: "secret-refresh",
		Expiry:       time.Now().Add(time.Hour), // valid → no refresh, endpoint untouched
	}
	src := oauth.NewSource(cfg, initial, nil)

	got, err := src.Value(t.Context())
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if string(got) != "initial-access" {
		t.Errorf("Value = %q, want the access token \"initial-access\"", got)
	}
	if string(got) == initial.RefreshToken {
		t.Errorf("Value returned the refresh token %q — it must never cross the boundary", got)
	}
}

// TestSource_RefreshYieldsFreshTokenAndOnChange: an expired token forces a
// refresh; the new access token is returned and onChange fires exactly once. A
// second read sees the still-valid refreshed token and must NOT re-fire (last
// detection).
func TestSource_RefreshYieldsFreshTokenAndOnChange(t *testing.T) {
	ts := newTokenServer(t)
	ts.setAccess("refreshed-access")
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	initial := &oauth2.Token{
		AccessToken:  "stale-access",
		RefreshToken: "refresh-1",
		Expiry:       time.Now().Add(-time.Hour), // expired → forces a refresh
	}

	var mu sync.Mutex
	var changed []string
	src := oauth.NewSource(cfg, initial, func(tok *oauth2.Token) {
		mu.Lock()
		changed = append(changed, tok.AccessToken)
		mu.Unlock()
	})

	got, err := src.Value(t.Context())
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if string(got) != "refreshed-access" {
		t.Errorf("Value = %q, want refreshed-access", got)
	}

	got2, err := src.Value(t.Context())
	if err != nil {
		t.Fatalf("Value #2: %v", err)
	}
	if string(got2) != "refreshed-access" {
		t.Errorf("Value #2 = %q, want the cached refreshed-access", got2)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(changed) != 1 {
		t.Fatalf("onChange fired %d times %v, want exactly 1", len(changed), changed)
	}
	if changed[0] != "refreshed-access" {
		t.Errorf("onChange token = %q, want refreshed-access", changed[0])
	}
}

// TestSource_TokenNeverExposedToGuest pins the API surface: Value(ctx)→[]byte is
// the ONLY exported method. No getter may hand out the refresh token or a
// *oauth2.Token — that reaches the host solely via onChange.
func TestSource_TokenNeverExposedToGuest(t *testing.T) {
	typ := reflect.TypeOf((*oauth.Source)(nil))

	var methods []string
	for i := range typ.NumMethod() {
		methods = append(methods, typ.Method(i).Name)
	}
	if len(methods) != 1 || methods[0] != "Value" {
		t.Fatalf("Source exported methods = %v, want exactly [Value] — no getter may leak the refresh token or *oauth2.Token", methods)
	}

	m, _ := typ.MethodByName("Value")
	if got, want := m.Type.Out(0), reflect.TypeOf([]byte(nil)); got != want {
		t.Errorf("Value first return = %v, want %v (only raw access bytes cross the boundary)", got, want)
	}

	var _ resolver = (*oauth.Source)(nil) // compile-time: exactly a byte Resolver
}

// TestSource_ValuePropagatesRefreshError: a failed refresh surfaces the error and
// does NOT fire onChange or advance last — proven by a subsequent SUCCESSFUL
// refresh still firing onChange (last was unchanged by the failure).
func TestSource_ValuePropagatesRefreshError(t *testing.T) {
	ts := newTokenServer(t)
	ts.setStatus(http.StatusBadRequest) // refresh fails
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	initial := &oauth2.Token{
		AccessToken:  "stale-access",
		RefreshToken: "refresh-1",
		Expiry:       time.Now().Add(-time.Hour), // expired → forces a refresh
	}

	var mu sync.Mutex
	fires := 0
	src := oauth.NewSource(cfg, initial, func(*oauth2.Token) {
		mu.Lock()
		fires++
		mu.Unlock()
	})

	if _, err := src.Value(t.Context()); err == nil {
		t.Fatal("Value: want the refresh error propagated, got nil")
	}
	mu.Lock()
	if fires != 0 {
		t.Errorf("onChange fired %d times on a refresh error, want 0", fires)
	}
	mu.Unlock()

	// Recover: a successful refresh must still fire onChange, proving the failure
	// left last untouched (a corrupted last would suppress it).
	ts.setStatus(http.StatusOK)
	ts.setAccess("recovered-access")
	got, err := src.Value(t.Context())
	if err != nil {
		t.Fatalf("Value after recovery: %v", err)
	}
	if string(got) != "recovered-access" {
		t.Errorf("Value = %q, want recovered-access", got)
	}
	mu.Lock()
	if fires != 1 {
		t.Errorf("onChange fired %d times overall, want 1 (only the successful refresh)", fires)
	}
	mu.Unlock()
}

// TestNewSource_RefreshUsesBackgroundContext: a canceled per-request ctx must NOT
// abort the refresh. NewSource pins a background ctx for refresh I/O, so the
// shared long-lived token survives any single request's cancellation.
func TestNewSource_RefreshUsesBackgroundContext(t *testing.T) {
	ts := newTokenServer(t)
	ts.setAccess("bg-access")
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	initial := &oauth2.Token{
		AccessToken:  "stale-access",
		RefreshToken: "refresh-1",
		Expiry:       time.Now().Add(-time.Hour), // expired → forces a refresh
	}
	src := oauth.NewSource(cfg, initial, nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // already canceled before the call

	got, err := src.Value(ctx)
	if err != nil {
		t.Fatalf("Value with a canceled request ctx: %v — refresh must ignore it", err)
	}
	if string(got) != "bg-access" {
		t.Errorf("Value = %q, want bg-access", got)
	}
}
