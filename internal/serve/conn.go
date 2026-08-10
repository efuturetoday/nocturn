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
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/speaker"
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
	// seq orders this connection against the device's others, assigned by the hub on add. It is how
	// the voice path tells a reconnect's winner from the loser still timing out.
	seq uint64
	log *slog.Logger

	// can is what this connection's device class may do, resolved once at accept so no command has
	// to re-interpret a class. See capability.go.
	can capabilities

	// capture is nil unless NOCTURN_VOICE_CAPTURE armed it. It writes uplink audio to WAV so a
	// voice can be enrolled through the device it will be recognised through.
	capture *capture

	// embedder turns speech into a speaker embedding, and is nil when this daemon has no model. It
	// is shared across every connection: the model is tens of megabytes and immutable once loaded.
	embedder *speaker.Embedder
	// listen recognises who is speaking for the session this connection opened, and is nil until one
	// is open — or stays nil when nothing here can recognise anybody.
	listen *listener

	// Two queues, one writer, control first. Audio is a steady twenty-five to fifty frames a second
	// where JSON is small and bursty, and the drop-when-full policy that suits the second ruins the
	// first: a chat event would be lost because audio filled the buffer. Separating them keeps the
	// loss where it is harmless — a dropped audio frame is a click, a dropped event is missing state.
	control chan any
	audio   chan []byte
	// closed releases anyone waiting to queue audio when this connection goes away. Without it a
	// writer blocked on a dead socket's queue would wait for a reader that is never coming back.
	closed chan struct{}
}

func newConn(ws *websocket.Conn, spaces map[string]*workspace.Workspace, devices *auth.Store, broker *hitl.Broker, hub *hub, device string, can capabilities, embedder *speaker.Embedder, log *slog.Logger) *conn {
	return &conn{
		ws: ws, spaces: spaces, devices: devices, broker: broker, hub: hub, device: device, can: can,
		embedder: embedder, log: log,
		control: make(chan any, 64),
		// Four frames — 80 ms. Short on purpose, and it is a latency budget rather than a buffer: this
		// queue sits AHEAD of the device's own, so everything in it is speech a barge-in can no longer
		// take back once written. The device buffers what needs buffering; this only needs enough to
		// keep the writer from stalling between frames.
		audio:  make(chan []byte, 4),
		closed: make(chan struct{}),
	}
}

// heartbeat is how often a connection is asked whether its device is still there, and how long an
// answer may take. It belongs to the daemon rather than the package so a test can run a fast one
// without changing the timing of every other connection in the binary.
type heartbeat struct{ every, wait time.Duration }

// defaultHeartbeat is what production runs: twenty-five seconds to notice a device that is gone.
//
// Something has to. A device that vanishes without a FIN — power cut, WiFi gone — leaves a connection
// that looks alive from here: the read loop is waiting for bytes that will never come, and nothing
// about that is distinguishable from a device with nothing to say. Left alone it is the OS keepalive
// that notices, after roughly two hours by default, and until then the device's voice session stays
// open and a live model bills for it.
//
// The budget is generous on purpose, and not because a pong is expected to be slow. The satellite
// answers from the WebSocket client's own task, which by its own contract may never block (link.h:
// that task drives the keepalive, so stalling it drops the link) — so a pong is prompt or the device
// is in trouble either way. What the ceiling buys is room for a congested link, and the cost of
// setting it too low is a working device disconnected mid-sentence. Nothing is won by tightening it:
// the point is to notice a device that is gone in seconds instead of hours, and ten of them is
// already three orders of magnitude better than the OS keepalive.
var defaultHeartbeat = heartbeat{every: 15 * time.Second, wait: 10 * time.Second}

// heartbeat closes a connection whose device has stopped answering.
//
// Ping writes a control frame and waits for the pong to be read by the read loop, so this must run
// alongside it — which it does. Both the write and the wait are bounded, so a writer stuck on a dead
// socket does not hold this one up: it fails, and failing is the answer.
func (c *conn) heartbeat(ctx context.Context) {
	beat := c.hub.beat
	t := time.NewTicker(beat.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case <-t.C:
		}
		ping, cancel := context.WithTimeout(ctx, beat.wait)
		err := c.ws.Ping(ping)
		cancel()
		if err != nil {
			// CloseNow rather than a graceful close: the peer is not answering, so waiting for it to
			// acknowledge a close frame would wait for the same silence again. Closing the socket ends
			// the read loop, which runs the teardown that ends the device's voice session.
			c.log.Info("no pong — closing a connection whose device is gone", "err", err)
			_ = c.ws.CloseNow()
			return
		}
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
	go c.heartbeat(ctx)
	if c.broker != nil {
		c.broker.Attach(ctx, c)
	}
	c.hub.add(c)
	defer func() {
		c.hub.remove(c)
		// Release anyone waiting to queue speech before anything else: a voice writer blocked on this
		// connection's queue must be let go, or the teardown below waits for it.
		close(c.closed)
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

// sendAudio queues one frame of speech, WAITING for room, and reports whether it was taken. It gives
// up only when abort is closed — the session ending — or when the connection does.
//
// Waiting rather than dropping is what makes the flow control work: this queue is short and it backs
// onto a socket the device stops reading while its speaker is full, so the wait is the speaker being
// full, propagated all the way here without a protocol to carry it.
func (c *conn) sendAudio(pcm []byte, abort <-chan struct{}) bool {
	select {
	case c.audio <- pcm:
		return true
	case <-abort:
		return false
	case <-c.closed:
		return false
	}
}

// dropAudio discards the queued speech of this connection and reports how many bytes went.
func (c *conn) dropAudio() int {
	n := 0
	for {
		select {
		case pcm := <-c.audio:
			n += len(pcm)
		default:
			return n
		}
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
	case "device":
		c.deviceCmd(ctx, env.Cmd, data)
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
	case "capture":
		c.captureCmd(ctx, env.Cmd, data)
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
