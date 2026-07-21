// Package serve exposes a workspace's chats over a WebSocket: one backend, many fronts
// (daemon-as-truth). The wire is tagged JSON — a client sends {cmd:"domain.action", …}, the server
// sends {type:"domain.action", …}. This file is the transport plumbing; each domain's messages and
// handlers live in their own file (chat.go, …).
package serve

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/workspace"
)

// Error is the one cross-domain event: a control-level failure.
type Error struct {
	Type string `json:"type"` // always "error"
	Text string `json:"text"`
}

// conn is one WebSocket client. It drives the workspace's chat manager and streams a single active
// chat's events back. Every outbound message goes through out — coder/websocket forbids concurrent
// writes, so one writer goroutine owns the socket's write side.
type conn struct {
	ws     *websocket.Conn
	space  *workspace.Workspace
	log    *slog.Logger
	out    chan any
	active *agentkit.Session
}

func newConn(ws *websocket.Conn, space *workspace.Workspace, log *slog.Logger) *conn {
	return &conn{ws: ws, space: space, log: log, out: make(chan any, 64)}
}

// serve runs the connection until ctx is cancelled or the socket closes: a writer goroutine drains
// out, and this goroutine reads and dispatches commands.
func (c *conn) serve(ctx context.Context) {
	c.log.Info("ws connection opened")
	go c.writer(ctx)
	defer func() {
		if c.active != nil {
			c.active.Close()
		}
		c.log.Info("ws connection closed")
	}()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.log.Debug("ws read ended", "err", err) // normal on client close
			return
		}
		c.dispatch(ctx, data)
	}
}

// writer serializes every outbound message onto the socket.
func (c *conn) writer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.out:
			if err := wsjson.Write(ctx, c.ws, msg); err != nil {
				c.log.Debug("ws write ended", "err", err)
				return
			}
		}
	}
}

// send queues a message for the writer (dropped if the buffer is full and the conn is dying).
func (c *conn) send(msg any) {
	select {
	case c.out <- msg:
	default:
	}
}

// dispatch routes a raw command by the domain of its "cmd" (domain.action).
func (c *conn) dispatch(ctx context.Context, data []byte) {
	var env struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		c.send(Error{Type: "error", Text: "bad command"})
		return
	}
	domain, _, _ := strings.Cut(env.Cmd, ".")
	switch domain {
	case "chat":
		c.chat(ctx, env.Cmd, data)
	default:
		c.send(Error{Type: "error", Text: "unknown domain: " + env.Cmd})
	}
}

// errText renders an error for the wire, "" when nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
