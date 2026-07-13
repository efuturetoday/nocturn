package ntfy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/hitl/ntfy"
)

// The listener turns "message" stream events into Resolve calls, and ignores
// non-message events (open, keepalive).
func TestListener_ResolvesTokenFromMessageEventsOnly(t *testing.T) {
	got := make(chan string, 4)
	resolve := func(tok string) error {
		got <- tok
		return nil
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server cannot stream")
			return
		}
		lines := []string{
			`{"event":"open","topic":"nocturn-responses"}`,
			`{"event":"message","topic":"nocturn-responses","message":"THE_TOKEN"}`,
			`{"event":"keepalive","topic":"nocturn-responses"}`,
		}
		for _, ln := range lines {
			io.WriteString(w, ln+"\n")
			flusher.Flush()
		}
		<-r.Context().Done() // keep the stream open until the client cancels
	}))
	defer srv.Close()

	l := ntfy.NewListener(srv.URL, "nocturn-responses", resolve)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()

	select {
	case tok := <-got:
		if tok != "THE_TOKEN" {
			t.Fatalf("resolved %q, want THE_TOKEN", tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not resolve a token in time")
	}

	// open and keepalive events must not have triggered a resolve.
	select {
	case extra := <-got:
		t.Fatalf("a non-message event triggered resolve: %q", extra)
	case <-time.After(100 * time.Millisecond):
	}
}
