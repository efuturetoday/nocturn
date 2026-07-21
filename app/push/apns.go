package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// APNSConfig configures the Apple Push adapter. Key is the token-signing key (a PKCS#8 EC private
// key downloaded from the Apple Developer portal) — either its PEM content or a path to the .p8;
// KeyID and TeamID identify it; BundleID is the app's bundle identifier (the apns-topic). Production
// selects the live APNs host vs the sandbox. OnBadToken, if set, is called with any token APNs
// reports permanently invalid, so the caller can drop it.
type APNSConfig struct {
	Key        string
	KeyID      string
	TeamID     string
	BundleID   string
	Production bool
	OnBadToken func(token string)

	// Host overrides the APNs host:port. Empty selects the live/sandbox host from Production.
	Host string
	// HTTPClient overrides the HTTP/2 client. Nil uses the default, which negotiates HTTP/2 via ALPN.
	HTTPClient *http.Client
	// now overrides the clock for the JWT iat (tests). Nil uses time.Now.
	now func() time.Time
}

// APNS delivers pushes to Apple Push Notification service over HTTP/2 with a token-based (ES256 JWT)
// authorization. It implements Sender. The provider auth token is cached and reused up to APNs'
// one-hour ceiling (regenerated at ~50 minutes). Safe for concurrent use.
type APNS struct {
	cfg    APNSConfig
	key    *ecdsa.PrivateKey
	host   string
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex // guards the cached provider token
	token   string
	tokenAt time.Time
}

var _ Sender = (*APNS)(nil)

const (
	apnsHostProd    = "api.push.apple.com"
	apnsHostSandbox = "api.sandbox.push.apple.com"
	apnsTokenMaxAge = 50 * time.Minute // < APNs' 1h ceiling
)

// APNSFromEnv builds the adapter from NOCTURN_APNS_KEY / _KEY_ID / _TEAM_ID / _BUNDLE_ID (and the
// optional NOCTURN_APNS_PRODUCTION). It returns (nil, nil) when NOCTURN_APNS_KEY is unset — push is
// simply off — so the caller falls back to a no-op.
func APNSFromEnv() (*APNS, error) {
	key := os.Getenv("NOCTURN_APNS_KEY")
	if key == "" {
		return nil, nil
	}
	return NewAPNS(APNSConfig{
		Key:        key,
		KeyID:      os.Getenv("NOCTURN_APNS_KEY_ID"),
		TeamID:     os.Getenv("NOCTURN_APNS_TEAM_ID"),
		BundleID:   os.Getenv("NOCTURN_APNS_BUNDLE_ID"),
		Production: os.Getenv("NOCTURN_APNS_PRODUCTION") == "true",
	})
}

// NewAPNS parses the .p8 signing key and builds the adapter. It fails if the key is missing, not
// PEM, or not an ECDSA PKCS#8 key.
func NewAPNS(cfg APNSConfig) (*APNS, error) {
	raw, err := loadKey(cfg.Key)
	if err != nil {
		return nil, fmt.Errorf("apns: read key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("apns: key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: parse key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("apns: key is %T, want *ecdsa.PrivateKey", parsed)
	}

	host := cfg.Host
	if host == "" {
		host = apnsHostSandbox
		if cfg.Production {
			host = apnsHostProd
		}
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	return &APNS{cfg: cfg, key: key, host: host, client: client, now: now}, nil
}

// loadKey reads the signing key from either inline PEM content (env may escape newlines) or a path.
func loadKey(k string) ([]byte, error) {
	if strings.Contains(k, "BEGIN") {
		return []byte(strings.ReplaceAll(k, `\n`, "\n")), nil
	}
	return os.ReadFile(k)
}

// Send delivers m to every token. A token APNs rejects as permanently invalid is reported via
// OnBadToken. It returns nil if at least one token was delivered, else an error — so a caller can
// treat "nobody reachable" as fail-closed.
func (a *APNS) Send(ctx context.Context, m Message, tokens []string) error {
	if len(tokens) == 0 {
		return errors.New("apns: no device tokens")
	}
	body, err := json.Marshal(payload(m))
	if err != nil {
		return fmt.Errorf("apns: marshal payload: %w", err)
	}
	jwt, err := a.providerToken()
	if err != nil {
		return err
	}

	var delivered int
	var lastErr error
	for _, tok := range tokens {
		switch err := a.push(ctx, jwt, tok, body); err {
		case nil:
			delivered++
		case errBadToken:
			if a.cfg.OnBadToken != nil {
				a.cfg.OnBadToken(tok)
			}
		default:
			lastErr = err
		}
	}
	if delivered == 0 {
		if lastErr != nil {
			return lastErr
		}
		return errors.New("apns: no token delivered")
	}
	return nil
}

// errBadToken marks a token APNs rejects permanently, so Send can prune it instead of failing.
var errBadToken = errors.New("apns: bad device token")

// push posts one notification for a single token.
func (a *APNS) push(ctx context.Context, jwt, token string, body []byte) error {
	url := "https://" + a.host + "/3/device/" + token
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("apns: build request: %w", err)
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", a.cfg.BundleID)
	req.Header.Set("apns-push-type", "alert")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("apns: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
	var r struct{ Reason string }
	_ = json.Unmarshal(respBody, &r)
	if resp.StatusCode == http.StatusGone || r.Reason == "BadDeviceToken" || r.Reason == "Unregistered" {
		return errBadToken
	}
	return fmt.Errorf("apns: status %d: %s", resp.StatusCode, r.Reason)
}

// providerToken returns the cached ES256 JWT, regenerating it past apnsTokenMaxAge.
func (a *APNS) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.now().Sub(a.tokenAt) < apnsTokenMaxAge {
		return a.token, nil
	}
	tok, err := a.signJWT()
	if err != nil {
		return "", err
	}
	a.token, a.tokenAt = tok, a.now()
	return tok, nil
}

// signJWT builds and ES256-signs the APNs provider token: header {alg,kid}, claims {iss,iat}.
func (a *APNS) signJWT() (string, error) {
	header := map[string]string{"alg": "ES256", "kid": a.cfg.KeyID}
	claims := map[string]any{"iss": a.cfg.TeamID, "iat": a.now().Unix()}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64(hb) + "." + b64(cb)

	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, sum[:])
	if err != nil {
		return "", fmt.Errorf("apns: sign jwt: %w", err)
	}
	// ES256 signature is the fixed-width big-endian r||s (32 bytes each for P-256).
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// payload wraps a Message in the APNs `aps` envelope plus any custom Data keys. Data is applied first
// so the reserved `aps` key can never be shadowed by a caller.
func payload(m Message) map[string]any {
	p := make(map[string]any, len(m.Data)+1)
	for k, v := range m.Data {
		p[k] = v
	}
	p["aps"] = map[string]any{
		"alert": map[string]string{"title": m.Title, "body": m.Body},
	}
	return p
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
