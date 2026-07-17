package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// The wsConn adapter round-trips a message through a real WebSocket: a handler accepts the
// upgrade, wraps it as an appserver.Conn, and echoes; the client sees its message back.
// This proves the coder/websocket ↔ appserver.Conn seam (Read/Write/Close) end to end.
func TestWSConn_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn := newWSConn(c)
		defer conn.Close()
		// Echo one message through the adapter's Read/Write.
		msg, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		_ = conn.Write(r.Context(), msg)
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	c, _, err := websocket.Dial(t.Context(), url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	if err := c.Write(t.Context(), websocket.MessageText, []byte(`{"cmd":"ping"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, got, err := c.Read(t.Context())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != `{"cmd":"ping"}` {
		t.Fatalf("echo = %q, want the message back", got)
	}
}
