package main

import (
	"context"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/internal/appserver"
)

// wsConn adapts a coder/websocket connection to appserver.Conn: framed JSON text messages,
// with ctx-honoring Read/Write (the app server relies on that to stop its goroutines on
// disconnect). This is the ONLY place the WebSocket library touches the app protocol — a
// future relay is a different appserver.Conn, nothing else changes.
type wsConn struct{ c *websocket.Conn }

var _ appserver.Conn = wsConn{}

// wsReadLimit bounds one inbound message. A persona edit or a long tool result fits; it is
// not a bulk-upload channel, so a generous-but-finite cap keeps a bad client from OOMing us.
const wsReadLimit = 1 << 20 // 1 MiB

func newWSConn(c *websocket.Conn) wsConn {
	c.SetReadLimit(wsReadLimit)
	return wsConn{c}
}

func (w wsConn) Read(ctx context.Context) ([]byte, error) {
	_, data, err := w.c.Read(ctx)
	return data, err
}

func (w wsConn) Write(ctx context.Context, msg []byte) error {
	return w.c.Write(ctx, websocket.MessageText, msg)
}

func (w wsConn) Close() error {
	return w.c.Close(websocket.StatusNormalClosure, "")
}
