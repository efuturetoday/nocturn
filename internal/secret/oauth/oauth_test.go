package oauth_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/efuturetoday/nocturn/internal/secret/oauth"
)

// resolver mirrors internal/secret.Resolver — the ONLY contract a credential source
// exposes to the injector. Source must satisfy it structurally, handing out just
// access-token bytes (never a *oauth2.Token, never the refresh token).
type resolver interface {
	Value(ctx context.Context) ([]byte, error)
}

var _ resolver = (*oauth.Source)(nil)

// tokenServer is a stub OAuth2 token endpoint. It captures the last exchange's
// code_verifier / code / grant_type and returns a configurable token (or an
// error status). Never a real browser or provider — the flow is driven by
// hitting the loopback callback directly.
type tokenServer struct {
	*httptest.Server

	mu       sync.Mutex
	verifier string
	code     string
	grant    string
	access   string
	refresh  string
	status   int
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	ts := &tokenServer{access: "access-1", refresh: "refresh-1", status: http.StatusOK}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ts.mu.Lock()
		ts.verifier = r.FormValue("code_verifier")
		ts.code = r.FormValue("code")
		ts.grant = r.FormValue("grant_type")
		status, access, refresh := ts.status, ts.access, ts.refresh
		ts.mu.Unlock()

		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"token_type":"Bearer","expires_in":3600}`, access, refresh)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) setAccess(v string) { ts.mu.Lock(); ts.access = v; ts.mu.Unlock() }
func (ts *tokenServer) setStatus(v int)    { ts.mu.Lock(); ts.status = v; ts.mu.Unlock() }

func (ts *tokenServer) exchangedCode() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.code
}

func (ts *tokenServer) sentVerifier() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.verifier
}

type authResult struct {
	tok *oauth2.Token
	err error
}

// launchAuthorize runs Authorize in a goroutine and returns the consent URL it
// hands to prompt, plus a channel delivering the eventual result. The caller
// drives the flow by hitting the loopback callback embedded in the consent URL.
func launchAuthorize(t *testing.T, ctx context.Context, cfg *oauth2.Config) (consentURL string, done <-chan authResult) {
	t.Helper()
	urlCh := make(chan string, 1)
	res := make(chan authResult, 1)
	go func() {
		tok, err := oauth.Authorize(ctx, cfg, "", func(u string) { urlCh <- u })
		res <- authResult{tok, err}
	}()
	select {
	case consentURL = <-urlCh:
	case r := <-res:
		t.Fatalf("Authorize returned before calling prompt: tok=%v err=%v", r.tok, r.err)
	case <-time.After(3 * time.Second):
		t.Fatal("prompt was never called")
	}
	return consentURL, res
}

func consentQuery(t *testing.T, consentURL string) url.Values {
	t.Helper()
	u, err := url.Parse(consentURL)
	if err != nil {
		t.Fatalf("parse consent URL %q: %v", consentURL, err)
	}
	return u.Query()
}

func callbackURL(t *testing.T, redirectURI string, q url.Values) string {
	t.Helper()
	u, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse redirect_uri %q: %v", redirectURI, err)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func getCallback(t *testing.T, redirectURI string, q url.Values) *http.Response {
	t.Helper()
	resp, err := http.Get(callbackURL(t, redirectURI, q))
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	return resp
}

// completeFlow finishes a launched ceremony with a valid callback and returns the
// result, so a test focused on the consent URL doesn't leak the listener.
func completeFlow(t *testing.T, consentURL string, done <-chan authResult) authResult {
	t.Helper()
	q := consentQuery(t, consentURL)
	resp := getCallback(t, q.Get("redirect_uri"), url.Values{
		"state": {q.Get("state")},
		"code":  {"done-code"},
	})
	_ = resp.Body.Close()
	return <-done
}

func TestAuthorize_LoopbackRedirectOnly(t *testing.T) {
	ts := newTokenServer(t)
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	consentURL, done := launchAuthorize(t, t.Context(), cfg)

	redirect := consentQuery(t, consentURL).Get("redirect_uri")
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatalf("parse redirect_uri %q: %v", redirect, err)
	}
	if u.Scheme != "http" {
		t.Errorf("redirect scheme = %q, want http", u.Scheme)
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split redirect host %q: %v", u.Host, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("redirect host = %q, want 127.0.0.1 (loopback only, never 0.0.0.0/non-loopback)", host)
	}
	if u.Path != "/callback" {
		t.Errorf("redirect path = %q, want /callback", u.Path)
	}

	if r := completeFlow(t, consentURL, done); r.err != nil {
		t.Fatalf("Authorize: %v", r.err)
	}
}

func TestAuthorize_StateCSRFGuard(t *testing.T) {
	ts := newTokenServer(t)
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	consentURL, done := launchAuthorize(t, t.Context(), cfg)

	q := consentQuery(t, consentURL)
	// A forged callback (wrong state) must NOT lead to a token exchange. State is verified
	// by the flow driver (here Loopback.Authorize) after the code is caught, not at the
	// socket — the same check the workspace applies when the companion app relays a code —
	// so the load-bearing guarantee is "no exchange on a state mismatch", asserted below.
	resp := getCallback(t, q.Get("redirect_uri"), url.Values{
		"state": {"forged-state"},
		"code":  {"attacker-code"},
	})
	resp.Body.Close()

	r := <-done
	if r.tok != nil {
		t.Errorf("got token %v, want none on state mismatch", r.tok)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "state mismatch") {
		t.Errorf("err = %v, want to contain \"state mismatch\"", r.err)
	}
	if got := ts.exchangedCode(); got != "" {
		t.Errorf("token endpoint saw code %q, want no exchange on a forged callback", got)
	}
}

func TestAuthorize_PKCEChallengeAndVerifier(t *testing.T) {
	ts := newTokenServer(t)
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	consentURL, done := launchAuthorize(t, t.Context(), cfg)

	q := consentQuery(t, consentURL)
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	challenge := q.Get("code_challenge")
	if challenge == "" {
		t.Fatal("consent URL carries no code_challenge")
	}

	if r := completeFlow(t, consentURL, done); r.err != nil {
		t.Fatalf("Authorize: %v", r.err)
	}

	verifier := ts.sentVerifier()
	if verifier == "" {
		t.Fatal("token exchange sent no code_verifier")
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if want != challenge {
		t.Errorf("S256(code_verifier) = %q, want it to match the challenge %q sent up front", want, challenge)
	}
}

func TestAuthorize_HappyPath(t *testing.T) {
	ts := newTokenServer(t)
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	consentURL, done := launchAuthorize(t, t.Context(), cfg)

	q := consentQuery(t, consentURL)
	resp := getCallback(t, q.Get("redirect_uri"), url.Values{
		"state": {q.Get("state")},
		"code":  {"the-auth-code"},
	})
	_ = resp.Body.Close()

	r := <-done
	if r.err != nil {
		t.Fatalf("Authorize: %v", r.err)
	}
	if r.tok.AccessToken != "access-1" {
		t.Errorf("access token = %q, want access-1", r.tok.AccessToken)
	}
	if r.tok.RefreshToken != "refresh-1" {
		t.Errorf("refresh token = %q, want refresh-1", r.tok.RefreshToken)
	}
	if got := ts.exchangedCode(); got != "the-auth-code" {
		t.Errorf("exchanged code = %q, want the-auth-code", got)
	}
}

func TestAuthorize_OfflineConsentParams(t *testing.T) {
	ts := newTokenServer(t)
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	consentURL, done := launchAuthorize(t, t.Context(), cfg)

	q := consentQuery(t, consentURL)
	if got := q.Get("access_type"); got != "offline" {
		t.Errorf("access_type = %q, want offline (needed for Google to return a refresh_token)", got)
	}
	if got := q.Get("prompt"); got != "consent" {
		t.Errorf("prompt = %q, want consent", got)
	}

	if r := completeFlow(t, consentURL, done); r.err != nil {
		t.Fatalf("Authorize: %v", r.err)
	}
}

func TestAuthorize_ProviderError(t *testing.T) {
	ts := newTokenServer(t)
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	consentURL, done := launchAuthorize(t, t.Context(), cfg)

	q := consentQuery(t, consentURL)
	resp := getCallback(t, q.Get("redirect_uri"), url.Values{
		"state": {q.Get("state")}, // correct state — the error branch is checked after
		"error": {"access_denied"},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback status = %d, want 400", resp.StatusCode)
	}

	r := <-done
	if r.tok != nil {
		t.Errorf("got token %v, want none when the provider denies", r.tok)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "authorization denied") || !strings.Contains(r.err.Error(), "access_denied") {
		t.Errorf("err = %v, want to name the denial (\"authorization denied\" + \"access_denied\")", r.err)
	}
	if got := ts.exchangedCode(); got != "" {
		t.Errorf("token endpoint saw code %q, want no exchange after a provider error", got)
	}
}

func TestAuthorize_MissingCode(t *testing.T) {
	ts := newTokenServer(t)
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	consentURL, done := launchAuthorize(t, t.Context(), cfg)

	q := consentQuery(t, consentURL)
	resp := getCallback(t, q.Get("redirect_uri"), url.Values{
		"state": {q.Get("state")}, // valid state, but no code
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no code") {
		t.Errorf("callback body = %q, want to contain \"no code\"", body)
	}

	r := <-done
	if r.tok != nil {
		t.Errorf("got token %v, want none when the callback omits code", r.tok)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "no code in callback") {
		t.Errorf("err = %v, want to contain \"no code in callback\"", r.err)
	}
}

// TestAuthorize_DuplicateCallbackNonBlocking fires two concurrent callbacks. The
// handler's non-blocking send must drop the second so its goroutine never wedges
// — a blocking send would leave the second handler stuck, hanging srv.Shutdown
// and Authorize forever. The test asserts Authorize still completes exactly once.
func TestAuthorize_DuplicateCallbackNonBlocking(t *testing.T) {
	ts := newTokenServer(t)
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")
	consentURL, done := launchAuthorize(t, t.Context(), cfg)

	q := consentQuery(t, consentURL)
	target := callbackURL(t, q.Get("redirect_uri"), url.Values{
		"state": {q.Get("state")},
		"code":  {"dup-code"},
	})

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp, err := http.Get(target); err == nil {
				_ = resp.Body.Close()
			}
		}()
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Authorize: %v", r.err)
		}
		if r.tok.AccessToken != "access-1" {
			t.Errorf("access token = %q, want access-1", r.tok.AccessToken)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Authorize hung — a duplicate callback wedged the handler")
	}
	wg.Wait()
}

// TestAuthorize_Timeout exercises the timeout branch. authTimeout is a fixed
// 3-minute const and Authorize binds a REAL loopback listener whose Serve
// goroutine blocks on network I/O (not durably blocked), so testing/synctest
// cannot advance the bubble clock — it deadlocks. Instead we pass a short parent
// deadline: context.WithTimeout composes to the earliest deadline, so the very
// same <-ctx.Done() branch fires and returns the identical "authorization timed
// out" error, in milliseconds of real time.
func TestAuthorize_Timeout(t *testing.T) {
	ts := newTokenServer(t)
	cfg := oauth.Provider("https://provider.example/auth", ts.URL, "client-id", "")

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	tok, err := oauth.Authorize(ctx, cfg, "", func(string) {}) // never call the callback
	if tok != nil {
		t.Errorf("got token %v, want none on timeout", tok)
	}
	if err == nil || !strings.Contains(err.Error(), "authorization timed out") {
		t.Errorf("err = %v, want to contain \"authorization timed out\"", err)
	}
}

func TestAuthorize_DefaultPromptPrintsURL(t *testing.T) {
	cfg := oauth.Provider("https://provider.example/auth", "https://provider.example/token", "client-id", "")

	// Redirect os.Stdout so we can observe the default prompt printing the URL —
	// it must PRINT, never exec a browser. Tests run sequentially (no t.Parallel),
	// so swapping the global is safe here.
	orig := os.Stdout
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = pw
	t.Cleanup(func() {
		os.Stdout = orig
		_ = pw.Close()
		_ = pr.Close()
	})

	ctx, cancel := context.WithCancel(t.Context())
	res := make(chan authResult, 1)
	go func() {
		tok, err := oauth.Authorize(ctx, cfg, "", nil) // nil prompt → default (print)
		res <- authResult{tok, err}
	}()

	rd := bufio.NewReader(pr)
	header, err := rd.ReadString('\n')
	if err != nil {
		t.Fatalf("read default-prompt header line: %v", err)
	}
	urlLine, err := rd.ReadString('\n')
	if err != nil {
		t.Fatalf("read default-prompt URL line: %v", err)
	}
	os.Stdout = orig // restore early so later test output is visible

	if !strings.Contains(header, "URL") {
		t.Errorf("default prompt header = %q, want it to instruct opening a URL", strings.TrimSpace(header))
	}
	consent := strings.TrimSpace(urlLine)
	u, err := url.Parse(consent)
	if err != nil || u.Scheme != "https" {
		t.Fatalf("default prompt printed %q, want the https consent URL", consent)
	}
	if u.Query().Get("code_challenge") == "" {
		t.Errorf("printed consent URL lacks PKCE challenge: %q", consent)
	}

	cancel() // let Authorize unwind (it will time out on the canceled ctx)
	<-res
}

// TestRandomState_UniqueHighEntropy observes the state param across many
// ceremonies (randomState is unexported): every state must be unique and decode
// to 32 bytes (256-bit entropy), so it can't be guessed to forge a callback.
func TestRandomState_UniqueHighEntropy(t *testing.T) {
	const n = 12
	seen := make(map[string]bool, n)
	for i := range n {
		ctx, cancel := context.WithCancel(t.Context())
		cfg := oauth.Provider("https://provider.example/auth", "https://provider.example/token", "client-id", "")
		consentURL, done := launchAuthorize(t, ctx, cfg)
		cancel()
		<-done

		state := consentQuery(t, consentURL).Get("state")
		if state == "" {
			t.Fatalf("iteration %d: empty state", i)
		}
		if seen[state] {
			t.Fatalf("iteration %d: duplicate state %q", i, state)
		}
		seen[state] = true

		raw, err := base64.RawURLEncoding.DecodeString(state)
		if err != nil {
			t.Fatalf("state %q is not base64url: %v", state, err)
		}
		if len(raw) != 32 {
			t.Errorf("state decodes to %d bytes, want 32 (256-bit)", len(raw))
		}
	}
}
