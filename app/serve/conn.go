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

	"github.com/efuturetoday/nocturn/app/auth"
	"github.com/efuturetoday/nocturn/app/hitl"
	"github.com/efuturetoday/nocturn/app/workspace"
)

// Error is the one cross-domain event: a control-level failure.
type Error struct {
	Type string `json:"type"` // always "error"
	Text string `json:"text"`
}

// newError builds an Error event with the discriminator set.
func newError(text string) Error { return Error{Type: "error", Text: text} }

// badRequest logs a CLIENT-fault (Warn) and returns the reason to the client — a malformed or
// unroutable command. Previously these vanished: the client saw the error, the operator saw nothing.
func (c *conn) badRequest(ctx context.Context, text string) {
	c.log.Warn("bad request", "reason", text)
	c.send(ctx, newError(text))
}

// failed logs a SERVER-fault (Error, with the underlying cause) and returns a reason to the client —
// an operation the server owns (transcript/list load) failed. The client gets the message; the
// operator gets the wrapped cause.
func (c *conn) failed(ctx context.Context, what string, err error) {
	c.log.Error(what+" failed", "err", err)
	c.send(ctx, newError(what+": "+err.Error()))
}

// conn is one WebSocket client. It is STATELESS w.r.t. chats: commands are id-addressed and routed to
// the Manager, and live chat events arrive via the daemon-wide hub (the Manager broadcasts every
// session's events to every device). A connection never owns or closes a session. Every outbound
// message goes through out — coder/websocket forbids concurrent writes, so one writer goroutine owns
// the socket's write side.
type conn struct {
	ws      *websocket.Conn
	spaces  map[string]*workspace.Workspace // the daemon's workspaces, by name
	devices *auth.Store
	broker  *hitl.Broker
	hub     *hub
	log     *slog.Logger
	out     chan any
}

func newConn(ws *websocket.Conn, spaces map[string]*workspace.Workspace, devices *auth.Store, broker *hitl.Broker, hub *hub, log *slog.Logger) *conn {
	return &conn{ws: ws, spaces: spaces, devices: devices, broker: broker, hub: hub, log: log, out: make(chan any, 64)}
}

// requireKind enforces that a store-addressed chat command names a valid store — "user" or "agent".
// Kind is mandatory (never defaulted): the client holds it per conversation and sends it on every
// chat.* command, so a missing or unknown kind is a client bug, rejected rather than silently treated
// as user chats.
func (c *conn) requireKind(ctx context.Context, kind string) bool {
	if kind == "user" || kind == "agent" {
		return true
	}
	c.badRequest(ctx, "missing or invalid kind (want \"user\" or \"agent\")")
	return false
}

// workspace resolves a workspace by name, writing an error and returning false if unknown.
func (c *conn) workspace(ctx context.Context, name string) (*workspace.Workspace, bool) {
	w, ok := c.spaces[name]
	if !ok {
		c.badRequest(ctx, "unknown workspace: "+name)
	}
	return w, ok
}

// serve runs the connection until ctx is cancelled or the socket closes: a writer goroutine drains
// out, and this goroutine reads and dispatches commands. While connected, it is attached to the
// broker to receive approval requests.
func (c *conn) serve(ctx context.Context) {
	c.log.Info("ws connection opened")
	go c.writer(ctx)
	c.broker.Attach(ctx, c)
	c.hub.add(c)
	defer func() {
		c.hub.remove(c)
		c.broker.Detach(c)
		// No session cleanup: sessions are server-owned and keep running past this connection.
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

// trySend enqueues a message without blocking, dropping it if the buffer is full — for broadcast
// hints (chat activity) a congested client can miss and recover from on its next resync.
func (c *conn) trySend(msg any) {
	select {
	case c.out <- msg:
	default:
	}
}

// send hands a message to the writer, applying backpressure: it blocks until the writer takes it or
// ctx is done (the connection tearing down, or the request that produced it). It never silently
// drops — a slow client slows the producer instead of losing stream events. ctx is passed, never
// stored: the broker supplies the connection's ctx when it presents an approval from another
// goroutine.
func (c *conn) send(ctx context.Context, msg any) {
	select {
	case c.out <- msg:
	case <-ctx.Done():
	}
}

// dispatch routes a raw command by the domain of its "cmd" (domain.action).
func (c *conn) dispatch(ctx context.Context, data []byte) {
	var env struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		c.badRequest(ctx, "bad command")
		return
	}
	domain, _, _ := strings.Cut(env.Cmd, ".")
	switch domain {
	case "chat":
		c.chat(ctx, env.Cmd, data)
	case "join":
		c.join(ctx, env.Cmd)
	case "approval":
		c.approval(ctx, env.Cmd, data)
	case "presence":
		c.presence(ctx, env.Cmd, data)
	case "workspace":
		c.workspaceCmd(ctx, env.Cmd)
	case "reminder":
		c.reminder(ctx, env.Cmd, data)
	case "agent":
		c.agentCmd(ctx, env.Cmd, data)
	default:
		c.badRequest(ctx, "unknown domain: "+env.Cmd)
	}
}

// errText renders an error for the wire, "" when nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
