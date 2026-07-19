package push_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/internal/push"
)

// writeTestKey generates a P-256 key and writes it as a PKCS#8 PEM (.p8), as Apple issues.
func writeTestKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

func TestAPNS_Send(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]http.Header{} // token -> the headers its request arrived with

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, "/3/device/")
		mu.Lock()
		seen[token] = r.Header.Clone()
		mu.Unlock()

		if token == "deadtoken" {
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["aps"]; !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	var pruned []string
	a, err := push.NewAPNS(push.APNSConfig{
		KeyPath:    writeTestKey(t),
		KeyID:      "KEY123",
		TeamID:     "TEAM123",
		BundleID:   "me.itexpert.nocturn",
		Host:       strings.TrimPrefix(srv.URL, "https://"),
		HTTPClient: srv.Client(),
		OnBadToken: func(tok string) { pruned = append(pruned, tok) },
	})
	if err != nil {
		t.Fatalf("NewAPNS: %v", err)
	}

	msg := push.Message{Title: "Approval needed", Body: "send email", Data: map[string]string{"type": "approval"}}
	if err := a.Send(context.Background(), msg, []string{"goodtoken", "deadtoken"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	mu.Lock()
	h := seen["goodtoken"]
	mu.Unlock()
	if got := h.Get("authorization"); !strings.HasPrefix(got, "bearer ") {
		t.Fatalf("authorization = %q, want a bearer JWT", got)
	}
	if got := h.Get("apns-topic"); got != "me.itexpert.nocturn" {
		t.Fatalf("apns-topic = %q, want the bundle id", got)
	}
	if got := h.Get("apns-push-type"); got != "alert" {
		t.Fatalf("apns-push-type = %q, want alert", got)
	}
	if len(pruned) != 1 || pruned[0] != "deadtoken" {
		t.Fatalf("pruned = %v, want [deadtoken]", pruned)
	}
}

func TestAPNS_Send_NoTokens(t *testing.T) {
	a, err := push.NewAPNS(push.APNSConfig{KeyPath: writeTestKey(t), KeyID: "k", TeamID: "t", BundleID: "b"})
	if err != nil {
		t.Fatalf("NewAPNS: %v", err)
	}
	if err := a.Send(context.Background(), push.Message{}, nil); err == nil {
		t.Fatal("Send(no tokens) = nil, want an error (nobody reachable)")
	}
}

func TestNewAPNS_BadKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.p8")
	if err := os.WriteFile(path, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := push.NewAPNS(push.APNSConfig{KeyPath: path}); err == nil {
		t.Fatal("NewAPNS(non-pem) = nil error, want a parse failure")
	}
}

// fakeSender records what it was asked to deliver — the double higher layers drive to prove an
// approval fires a wake push without a real provider.
type fakeSender struct {
	msgs   []push.Message
	tokens [][]string
}

func (f *fakeSender) Send(_ context.Context, m push.Message, tokens []string) error {
	f.msgs = append(f.msgs, m)
	f.tokens = append(f.tokens, tokens)
	return nil
}

var _ push.Sender = (*fakeSender)(nil)

func TestFakeSender_Delivers(t *testing.T) {
	f := &fakeSender{}
	_ = f.Send(context.Background(), push.Message{Title: "Approve?", Body: "Send email"}, []string{"tok-a"})
	if len(f.msgs) != 1 || f.msgs[0].Title != "Approve?" || len(f.tokens[0]) != 1 {
		t.Fatalf("sender did not record the delivery: %+v", f)
	}
}
