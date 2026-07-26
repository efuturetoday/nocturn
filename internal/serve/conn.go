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

	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/workspace"
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
	// broker is nil when this device may not answer approvals. Absence rather than a check: the
	// connection then has nothing to approve WITH, the same way a plugin outside its cage finds the
	// tool missing rather than refused.
	broker *hitl.Broker
	hub    *hub
	device string // which device this connection is, for addressed delivery. Identity, not session state.
	log    *slog.Logger

	// Two queues, one writer, control first. Audio is a steady twenty-five to fifty frames a second
	// where JSON is small and bursty, and the drop-when-full policy that suits the second ruins the
	// first: a chat event would be lost because audio filled the buffer. Separating them keeps the
	// loss where it is harmless — a dropped audio frame is a click, a dropped event is missing state.
	control chan any
	audio   chan []byte
}

func newConn(ws *websocket.Conn, spaces map[string]*workspace.Workspace, devices *auth.Store, broker *hitl.Broker, hub *hub, device string, log *slog.Logger) *conn {
	return &conn{
		ws: ws, spaces: spaces, devices: devices, broker: broker, hub: hub, device: device, log: log,
		control: make(chan any, 64),
		// Roughly a second at the rates a satellite sends. Deeper only delays the moment a congested
		// link is noticed, and stale audio has no value.
		audio: make(chan []byte, 48),
	}
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
	if c.broker != nil {
		c.broker.Attach(ctx, c)
	}
	c.hub.add(c)
	defer func() {
		c.hub.remove(c)
		if c.broker != nil {
			c.broker.Detach(c)
		}
		// Chat sessions are server-owned and keep running past this connection — their answer is
		// still worth having when the client returns. A spoken one is not: audio has nobody to play
		// to and a live model bills for every second, so it ends with the device.
		c.endVoice()
		c.log.Info("ws connection closed")
	}()
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			c.log.Debug("ws read ended", "err", err) // normal on client close
			return
		}
		// Binary frames are the only ones that are not commands: they are microphone audio, and they
		// carry no envelope because a device has one conversation at a time.
		if typ == websocket.MessageBinary {
			c.audioIn(data)
			continue
		}
		c.dispatch(ctx, data)
	}
}

// writer serializes every outbound message onto the socket. coder/websocket forbids concurrent
// writes, so this is the only goroutine that touches the write side.
//
// Control is drained before audio, always: an approval or a chat event waiting behind a second of
// buffered speech would arrive too late to matter, while audio delayed by a JSON frame is
// imperceptible.
func (c *conn) writer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.control:
			if !c.writeJSON(ctx, msg) {
				return
			}
		default:
			select {
			case <-ctx.Done():
				return
			case msg := <-c.control:
				if !c.writeJSON(ctx, msg) {
					return
				}
			case pcm := <-c.audio:
				if err := c.ws.Write(ctx, websocket.MessageBinary, pcm); err != nil {
					c.log.Debug("ws audio write ended", "err", err)
					return
				}
			}
		}
	}
}

// writeJSON reports whether the writer should keep going.
func (c *conn) writeJSON(ctx context.Context, msg any) bool {
	if err := wsjson.Write(ctx, c.ws, msg); err != nil {
		c.log.Debug("ws write ended", "err", err)
		return false
	}
	return true
}

// sendAudio queues one chunk of speech, dropping it when the queue is full rather than waiting.
// Blocking here would stall whatever produced the audio, and a late frame is worth less than a
// missing one: the listener hears a click either way, but a stalled producer stops the conversation.
func (c *conn) sendAudio(pcm []byte) {
	select {
	case c.audio <- pcm:
	default:
		c.log.Debug("audio frame dropped, link congested")
	}
}

// trySend enqueues a message without blocking, dropping it if the buffer is full — for broadcast
// hints (chat activity) a congested client can miss and recover from on its next resync.
func (c *conn) trySend(msg any) {
	select {
	case c.control <- msg:
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
	case c.control <- msg:
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
	case "auth":
		c.auth(ctx, env.Cmd, data)
	case "voice":
		c.voice(ctx, env.Cmd, data)
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
