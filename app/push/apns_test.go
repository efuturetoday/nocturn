package push

// White-box tests: the APNs adapter is driven through an httptest TLS stub standing in for the real
// api.push.apple.com, and the JWT clock is injected via the unexported cfg.now hook (no synctest).
// The signing key is a throwaway P-256 key generated per test — nothing here talks to Apple.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedTime is an arbitrary, stable wall clock for deterministic JWT iat claims.
var fixedTime = time.Unix(1_700_000_000, 0)

// newTestKey generates a throwaway P-256 signing key and returns it plus its PKCS#8 PEM encoding.
func newTestKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// capturedReq is one request the stub APNs server observed.
type capturedReq struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// stubResp is the canned reply for a token: a status and an optional APNs reason string.
type stubResp struct {
	status int
	reason string
}

// stubAPNs is an in-memory stand-in for APNs. It records every request and replies per-token from
// statuses (keyed by the trailing token in /3/device/<token>), defaulting to 200 OK.
type stubAPNs struct {
	mu       sync.Mutex
	reqs     []capturedReq
	statuses map[string]stubResp
}

func (s *stubAPNs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.reqs = append(s.reqs, capturedReq{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
	resp, ok := s.statuses[path.Base(r.URL.Path)]
	s.mu.Unlock()
	if !ok || resp.status == 0 {
		resp = stubResp{status: http.StatusOK}
	}
	if resp.status == http.StatusOK {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(resp.status)
	_ = json.NewEncoder(w).Encode(map[string]string{"reason": resp.reason})
}

func (s *stubAPNs) captured() []capturedReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedReq(nil), s.reqs...)
}

// newTestAPNS wires cfg to a fresh TLS stub server. Host/HTTPClient are overridden to hit the stub;
// an unset cfg.now defaults to the fixed clock. Key must already be set on cfg.
func newTestAPNS(t *testing.T, stub http.Handler, cfg APNSConfig) *APNS {
	t.Helper()
	ts := httptest.NewTLSServer(stub)
	t.Cleanup(ts.Close)
	cfg.Host = strings.TrimPrefix(ts.URL, "https://")
	cfg.HTTPClient = ts.Client()
	if cfg.now == nil {
		cfg.now = func() time.Time { return fixedTime }
	}
	a, err := NewAPNS(cfg)
	if err != nil {
		t.Fatalf("NewAPNS: %v", err)
	}
	return a
}

func TestAPNSFromEnv_UnsetKeyReturnsNilNil(t *testing.T) {
	t.Setenv("NOCTURN_APNS_KEY", "") // unset — push is simply off

	a, err := APNSFromEnv()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if a != nil {
		t.Fatalf("APNS = %v, want nil", a)
	}
}

func TestNewAPNS_ProductionFlagSelectsHost(t *testing.T) {
	_, keyPEM := newTestKey(t)

	tests := []struct {
		name       string
		production bool
		wantHost   string
	}{
		{name: "sandbox by default", production: false, wantHost: apnsHostSandbox},
		{name: "production flag selects live host", production: true, wantHost: apnsHostProd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewAPNS(APNSConfig{Key: keyPEM, Production: tt.production})
			if err != nil {
				t.Fatalf("NewAPNS: %v", err)
			}
			if a.host != tt.wantHost {
				t.Errorf("host = %q, want %q", a.host, tt.wantHost)
			}
		})
	}
}

func TestNewAPNS_HostOverride(t *testing.T) {
	t.Parallel()
	_, keyPEM := newTestKey(t)

	a, err := NewAPNS(APNSConfig{Key: keyPEM, Production: true, Host: "localhost:9999"})
	if err != nil {
		t.Fatalf("NewAPNS: %v", err)
	}
	if a.host != "localhost:9999" {
		t.Errorf("host = %q, want the explicit override localhost:9999", a.host)
	}
}

func TestNewAPNS_KeyErrors(t *testing.T) {
	t.Parallel()

	// A valid PEM block whose bytes are not a PKCS#8 key.
	notPKCS8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not-a-key")}))

	// A valid PKCS#8 key that is RSA, not ECDSA.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	rsaDER, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatalf("marshal rsa key: %v", err)
	}
	rsaPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsaDER}))

	tests := []struct {
		name    string
		key     string
		wantSub string
	}{
		{name: "not PEM", key: "xxBEGINxx", wantSub: "key is not PEM"},
		{name: "not PKCS8", key: notPKCS8, wantSub: "parse key"},
		{name: "not ECDSA", key: rsaPEM, wantSub: "want *ecdsa.PrivateKey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewAPNS(APNSConfig{Key: tt.key})
			if err == nil {
				t.Fatalf("err = nil, want error containing %q", tt.wantSub)
			}
			if a != nil {
				t.Errorf("APNS = %v, want nil on error", a)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func TestLoadKey_InlineVsPath(t *testing.T) {
	t.Parallel()

	t.Run("inline PEM unescapes newlines", func(t *testing.T) {
		t.Parallel()
		inline := `-----BEGIN TEST-----\nQUJD\n-----END TEST-----` // literal backslash-n, as env delivers it
		got, err := loadKey(inline)
		if err != nil {
			t.Fatalf("loadKey: %v", err)
		}
		want := strings.ReplaceAll(inline, `\n`, "\n")
		if string(got) != want {
			t.Errorf("loadKey = %q, want %q", got, want)
		}
		if !strings.Contains(string(got), "\n") {
			t.Error("expected real newlines after unescaping")
		}
	})

	t.Run("path reads file contents", func(t *testing.T) {
		t.Parallel()
		file := filepath.Join(t.TempDir(), "key.p8")
		const contents = "file-contents-no-begin-marker"
		if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
			t.Fatalf("write temp key: %v", err)
		}
		got, err := loadKey(file)
		if err != nil {
			t.Fatalf("loadKey: %v", err)
		}
		if string(got) != contents {
			t.Errorf("loadKey = %q, want %q", got, contents)
		}
	})
}

func TestAPNS_Send_DeliversToAllTokens(t *testing.T) {
	t.Parallel()
	_, keyPEM := newTestKey(t)
	stub := &stubAPNs{}
	a := newTestAPNS(t, stub, APNSConfig{Key: keyPEM, BundleID: "com.example.app"})

	tokens := []string{"tokA", "tokB", "tokC"}
	if err := a.Send(context.Background(), Message{Title: "t", Body: "b"}, tokens); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := stub.captured()
	if len(got) != len(tokens) {
		t.Fatalf("got %d requests, want %d", len(got), len(tokens))
	}
	seen := map[string]bool{}
	for _, r := range got {
		if r.method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.method)
		}
		wantPrefix := "/3/device/"
		if !strings.HasPrefix(r.path, wantPrefix) {
			t.Errorf("path = %q, want prefix %q", r.path, wantPrefix)
		}
		seen[strings.TrimPrefix(r.path, wantPrefix)] = true
	}
	for _, tok := range tokens {
		if !seen[tok] {
			t.Errorf("token %q was not delivered to", tok)
		}
	}
}

func TestAPNS_Send_NilWhenAtLeastOneDelivered(t *testing.T) {
	t.Parallel()
	_, keyPEM := newTestKey(t)
	// tokBad returns a non-bad-token failure (400); tokGood succeeds. Partial success => nil.
	stub := &stubAPNs{statuses: map[string]stubResp{
		"tokBad": {status: http.StatusBadRequest, reason: "PayloadTooLarge"},
	}}
	a := newTestAPNS(t, stub, APNSConfig{Key: keyPEM})

	if err := a.Send(context.Background(), Message{Body: "b"}, []string{"tokBad", "tokGood"}); err != nil {
		t.Fatalf("Send = %v, want nil (one token delivered)", err)
	}
}

func TestAPNS_Send_ErrorWhenNoneDelivered(t *testing.T) {
	t.Parallel()
	_, keyPEM := newTestKey(t)

	t.Run("empty token slice", func(t *testing.T) {
		t.Parallel()
		a := newTestAPNS(t, &stubAPNs{}, APNSConfig{Key: keyPEM})
		err := a.Send(context.Background(), Message{Body: "b"}, nil)
		if err == nil || err.Error() != "apns: no device tokens" {
			t.Fatalf("err = %v, want \"apns: no device tokens\"", err)
		}
	})

	t.Run("all tokens bad", func(t *testing.T) {
		t.Parallel()
		stub := &stubAPNs{statuses: map[string]stubResp{
			"a": {status: http.StatusGone},
			"b": {status: http.StatusGone},
		}}
		a := newTestAPNS(t, stub, APNSConfig{Key: keyPEM})
		err := a.Send(context.Background(), Message{Body: "b"}, []string{"a", "b"})
		if err == nil || err.Error() != "apns: no token delivered" {
			t.Fatalf("err = %v, want \"apns: no token delivered\"", err)
		}
	})
}

func TestAPNS_Send_BadTokenInvokesOnBadTokenNonFatal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		resp stubResp
	}{
		{name: "http 410 Gone", resp: stubResp{status: http.StatusGone}},
		{name: "reason BadDeviceToken", resp: stubResp{status: http.StatusBadRequest, reason: "BadDeviceToken"}},
		{name: "reason Unregistered", resp: stubResp{status: http.StatusBadRequest, reason: "Unregistered"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, keyPEM := newTestKey(t)

			var mu sync.Mutex
			var pruned []string
			stub := &stubAPNs{statuses: map[string]stubResp{"badTok": tt.resp}}
			a := newTestAPNS(t, stub, APNSConfig{
				Key: keyPEM,
				OnBadToken: func(tok string) {
					mu.Lock()
					pruned = append(pruned, tok)
					mu.Unlock()
				},
			})

			// goodTok delivers, so Send stays non-fatal while badTok is pruned.
			if err := a.Send(context.Background(), Message{Body: "b"}, []string{"goodTok", "badTok"}); err != nil {
				t.Fatalf("Send = %v, want nil (one token delivered)", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(pruned) != 1 || pruned[0] != "badTok" {
				t.Fatalf("OnBadToken called with %v, want [badTok]", pruned)
			}
		})
	}
}

func TestAPNS_Send_ContextCancelled(t *testing.T) {
	t.Parallel()
	_, keyPEM := newTestKey(t)
	a := newTestAPNS(t, &stubAPNs{}, APNSConfig{Key: keyPEM})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the request goes out

	err := a.Send(ctx, Message{Body: "b"}, []string{"tok"})
	if err == nil {
		t.Fatal("Send = nil, want a context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Send err = %v, want it to wrap context.Canceled", err)
	}
}

func TestAPNS_Push_HeadersAndAuth(t *testing.T) {
	t.Parallel()
	key, keyPEM := newTestKey(t)
	stub := &stubAPNs{}
	a := newTestAPNS(t, stub, APNSConfig{Key: keyPEM, KeyID: "KID123", TeamID: "TEAM99", BundleID: "com.example.app"})

	if err := a.Send(context.Background(), Message{Title: "t", Body: "b"}, []string{"tok"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := stub.captured()
	if len(got) != 1 {
		t.Fatalf("got %d requests, want 1", len(got))
	}
	h := got[0].header

	if want := "alert"; h.Get("apns-push-type") != want {
		t.Errorf("apns-push-type = %q, want %q", h.Get("apns-push-type"), want)
	}
	if want := "com.example.app"; h.Get("apns-topic") != want {
		t.Errorf("apns-topic = %q, want %q", h.Get("apns-topic"), want)
	}
	auth := h.Get("authorization")
	if !strings.HasPrefix(auth, "bearer ") {
		t.Fatalf("authorization = %q, want it to start with \"bearer \"", auth)
	}
	// The bearer must be a valid ES256 JWT signed by our key.
	verifyJWT(t, strings.TrimPrefix(auth, "bearer "), key, "KID123", "TEAM99")
}

func TestAPNS_Push_UnknownStatusSurfacesReason(t *testing.T) {
	t.Parallel()
	_, keyPEM := newTestKey(t)

	called := false
	stub := &stubAPNs{statuses: map[string]stubResp{
		"tok": {status: http.StatusTooManyRequests, reason: "TooManyRequests"},
	}}
	a := newTestAPNS(t, stub, APNSConfig{Key: keyPEM, OnBadToken: func(string) { called = true }})

	err := a.Send(context.Background(), Message{Body: "b"}, []string{"tok"})
	if err == nil {
		t.Fatal("Send = nil, want an error surfacing the status")
	}
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "TooManyRequests") {
		t.Errorf("err = %q, want it to mention status 429 and the reason", err.Error())
	}
	if called {
		t.Error("OnBadToken called for a non-bad-token status")
	}
}

func TestAPNS_ProviderToken_CachedUntilMaxAge(t *testing.T) {
	t.Parallel()
	_, keyPEM := newTestKey(t)

	var clockMu sync.Mutex
	nowVal := fixedTime
	a, err := NewAPNS(APNSConfig{
		Key: keyPEM, KeyID: "K", TeamID: "T",
		now: func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return nowVal },
	})
	if err != nil {
		t.Fatalf("NewAPNS: %v", err)
	}
	advance := func(d time.Duration) { clockMu.Lock(); nowVal = nowVal.Add(d); clockMu.Unlock() }

	first, err := a.providerToken()
	if err != nil {
		t.Fatalf("providerToken: %v", err)
	}

	// Just under the ceiling: the cached token is reused verbatim.
	advance(apnsTokenMaxAge - time.Second)
	cached, err := a.providerToken()
	if err != nil {
		t.Fatalf("providerToken: %v", err)
	}
	if cached != first {
		t.Errorf("token regenerated before maxAge: got a new token at %v", apnsTokenMaxAge-time.Second)
	}

	// At the ceiling exactly (Sub == maxAge, not < maxAge): regenerated.
	advance(time.Second)
	rotated, err := a.providerToken()
	if err != nil {
		t.Fatalf("providerToken: %v", err)
	}
	if rotated == first {
		t.Errorf("token not regenerated at maxAge boundary (%v)", apnsTokenMaxAge)
	}
}

func TestAPNS_SignJWT_ES256Structure(t *testing.T) {
	t.Parallel()
	key, keyPEM := newTestKey(t)
	a, err := NewAPNS(APNSConfig{Key: keyPEM, KeyID: "MYKID", TeamID: "MYTEAM", now: func() time.Time { return fixedTime }})
	if err != nil {
		t.Fatalf("NewAPNS: %v", err)
	}

	jwt, err := a.signJWT()
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}
	verifyJWT(t, jwt, key, "MYKID", "MYTEAM")
}

func TestAPNS_Payload_NoSecretLeaked(t *testing.T) {
	t.Parallel()
	_, keyPEM := newTestKey(t)
	stub := &stubAPNs{}
	a := newTestAPNS(t, stub, APNSConfig{Key: keyPEM, KeyID: "KID", TeamID: "TEAM", BundleID: "b"})

	msg := Message{Title: "hi", Body: "there", Data: map[string]string{"type": "approval", "chatId": "c-42"}}
	if err := a.Send(context.Background(), msg, []string{"tok"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := stub.captured()
	if len(got) != 1 {
		t.Fatalf("got %d requests, want 1", len(got))
	}
	jwt := strings.TrimPrefix(got[0].header.Get("authorization"), "bearer ")
	body := got[0].body

	// The provider JWT is a header-only credential — it must never appear in the payload body.
	if jwt != "" && strings.Contains(string(body), jwt) {
		t.Error("provider JWT leaked into the request body")
	}
	// Body top-level keys are exactly aps + the Data keys — nothing else (no iss/kid/token).
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("body is not a JSON object: %v", err)
	}
	wantKeys := map[string]bool{"aps": true, "type": true, "chatId": true}
	for k := range top {
		if !wantKeys[k] {
			t.Errorf("unexpected top-level body key %q", k)
		}
	}
	for k := range wantKeys {
		if _, ok := top[k]; !ok {
			t.Errorf("missing expected body key %q", k)
		}
	}
}

func TestPayload_DataCannotShadowAPS(t *testing.T) {
	t.Parallel()
	// A caller trying to inject its own "aps" via Data must not override the real envelope.
	p := payload(Message{Title: "real", Body: "body", Data: map[string]string{"aps": "evil"}})

	aps, ok := p["aps"].(map[string]any)
	if !ok {
		t.Fatalf("aps = %T, want the reserved map envelope (not the shadowing string)", p["aps"])
	}
	if _, ok := aps["alert"]; !ok {
		t.Error("aps envelope missing its alert payload")
	}
}

func TestPayload_CarriesDeepLinkData(t *testing.T) {
	t.Parallel()
	p := payload(Message{Title: "t", Body: "b", Data: map[string]string{"chatId": "chat-7"}})

	if p["chatId"] != "chat-7" {
		t.Errorf("chatId = %v, want it to survive into the payload for deep-linking", p["chatId"])
	}
}

// verifyJWT asserts that raw is a three-part ES256 JWT whose header carries kid, whose claims carry
// iss and a numeric iat, and whose 64-byte r||s signature validates against key over the signing input.
func verifyJWT(t *testing.T, raw string, key *ecdsa.PrivateKey, wantKID, wantISS string) {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts, want 3", len(parts))
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	decodeJWTSegment(t, parts[0], &header)
	if header.Alg != "ES256" {
		t.Errorf("alg = %q, want ES256", header.Alg)
	}
	if header.Kid != wantKID {
		t.Errorf("kid = %q, want %q", header.Kid, wantKID)
	}

	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
	}
	decodeJWTSegment(t, parts[1], &claims)
	if claims.Iss != wantISS {
		t.Errorf("iss = %q, want %q", claims.Iss, wantISS)
	}
	if claims.Iat == 0 {
		t.Error("iat missing or zero")
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want 64 (r||s for P-256)", len(sig))
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key.PublicKey, sum[:], r, s) {
		t.Error("ES256 signature does not verify against the signing key")
	}
}

func decodeJWTSegment(t *testing.T, seg string, v any) {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("decode jwt segment: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal jwt segment: %v", err)
	}
}
