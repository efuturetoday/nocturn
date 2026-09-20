package workspace

import (
	"context"
	"log/slog"
	"testing"

	"github.com/efuturetoday/nocturn/internal/secret"
)

// A discovery pass registers refreshing credential sources (OAuth) and then rebinds every extension's
// credentials. Those two steps must not fight: if the rebind dropped and re-seeded resolvers, a
// refreshing source would be replaced by a static read of the STORED value — which for an OAuth
// credential is the serialized token, refresh token and all, stamped into an Authorization header.
//
// This is the reload that used to break it: the workspace opens, something registers a source, and a
// second pass runs.
func TestReload_KeepsARefreshingCredentialSource(t *testing.T) {
	dir := t.TempDir()
	m := testMaster(t)
	w, err := Open(Host{LLM: llmStub{}, Master: m, Log: slog.New(slog.DiscardHandler)}, "test", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)

	// A plugin with a declared credential, and a refreshing source registered for its key — the shape
	// registerOAuth produces for an authorized account.
	writePlugin(t, dir, "weather", "now", []secret.Binding{{Secret: "api_key", Host: "api.example.com", Header: "Authorization"}})
	if err := w.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	const key = "ext:weather@api.example.com/api_key"
	w.sec.injector.SetResolver(key, refreshing{})

	if err := w.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	req := &secret.Request{Method: "GET", Headers: map[string]string{}}
	ctx := secret.WithOwner(context.Background(), "ext:weather")
	if _, err := w.sec.injector.InjectMatching(ctx, req, "api.example.com"); err != nil {
		t.Fatalf("InjectMatching: %v", err)
	}
	if got := req.Headers["Authorization"]; got != "fresh" {
		t.Fatalf("Authorization = %q, want the refreshed value; a reload replaced the refreshing source", got)
	}
}

// refreshing stands in for an OAuth source: it yields the current access token, never the stored blob.
type refreshing struct{}

func (refreshing) Value(context.Context) ([]byte, error) { return []byte("fresh"), nil }
