package serve

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/speaker"
	"github.com/efuturetoday/nocturn/internal/tools"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// hub is the set of live connections, so a chat change (a turn save, a markRead) can be broadcast to
// every device — the daemon is the truth, the WebSocket a sink.
//
// seq orders connections by arrival so the voice path can name the newest one. A counter rather than
// a timestamp: what is needed is "which of these two arrived later", which a monotonic count answers
// exactly and a clock only approximately.
type hub struct {
	// beat is the liveness check every connection of this daemon runs. Written once at construction
	// and read-only after, so a connection reads it without synchronising.
	beat heartbeat

	mu    sync.Mutex
	conns map[*conn]struct{}
	seq   uint64
}

func newHub(beat heartbeat) *hub { return &hub{beat: beat, conns: map[*conn]struct{}{}} }

func (h *hub) add(c *conn) {
	h.mu.Lock()
	h.seq++
	c.seq = h.seq
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}
func (h *hub) remove(c *conn) { h.mu.Lock(); delete(h.conns, c); h.mu.Unlock() }

// sendAudio delivers one frame of speech to a device's NEWEST connection, WAITING for room, and
// reports whether it was taken. It gives up when abort is closed.
//
// Waiting is the point. The queue behind a connection is short and the device stops reading its
// socket while its speaker is full, so a full queue means the speaker is full — which is exactly when
// nothing more should be sent. Dropping instead would put a gap in the middle of a sentence to solve
// a problem that solves itself by waiting.
//
// Broadcast is right for chat — every device shows the same conversation — and wrong for audio: a
// phone should not play what the speaker in the hallway is hearing.
//
// The NEWEST rather than all of them, and this is load-bearing given that waiting is. A device holds
// two connections while a dead one times out, and a dead one is dead in the way that matters here:
// its writer is blocked in a socket write that will not return, so its queue fills and never drains.
// Sending to every connection means waiting on that queue — with the session live, abort silent and
// the connection not yet closed, the wait has no end, and the frame never reaches the socket that
// works. Fanning out a stream that must block is fanning out the stall with it.
func (h *hub) sendAudio(device string, pcm []byte, abort <-chan struct{}) bool {
	c := h.newest(device)
	if c == nil {
		return false
	}
	return c.sendAudio(pcm, abort)
}

// control delivers a JSON message to a device's newest connection. Unlike send it goes on the control
// queue, so it overtakes whatever speech is already buffered — which matters for the one message
// that tells a device to throw that speech away.
//
// The same connection speech went to, necessarily: an interrupt is only meaningful to whoever holds
// the audio it cancels.
func (h *hub) control(device string, msg any) {
	if c := h.newest(device); c != nil {
		c.trySend(msg)
	}
}

// dropAudio empties the queued speech of a device's newest connection and reports how much was
// discarded.
//
// An interrupt that only stopped the sender would leave whatever is already queued to arrive after
// the device has flushed — it would obediently discard its own buffer and then be handed the same
// speech again.
func (h *hub) dropAudio(device string) int {
	if c := h.newest(device); c != nil {
		return c.dropAudio()
	}
	return 0
}

// newest returns the most recently added connection of one device, or nil if it has none.
//
// A device has one at a time in principle and two in practice, for as long as a dropped connection
// takes to time out. The later one is the one the device is actually reading.
func (h *hub) newest(device string) *conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	var newest *conn
	for c := range h.conns {
		if c.device == device && (newest == nil || c.seq > newest.seq) {
			newest = c
		}
	}
	return newest
}

// countOf reports how many connections a device holds.
func (h *hub) countOf(device string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for c := range h.conns {
		if c.device == device {
			n++
		}
	}
	return n
}

// broadcast sends msg to every connection without blocking (a slow one drops it and resyncs).
func (h *hub) broadcast(msg any) {
	h.mu.Lock()
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.trySend(msg)
	}
}

// bootstrapTTL is how long a first-device pairing code stays valid.
const bootstrapTTL = 5 * time.Minute

// cors wraps a handler to allow any origin (the companion app's origin is not fixed) and answers the
// preflight OPTIONS with 204. Pairing endpoints carry no cookies, so allow-all is safe here.
func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// Serve runs a WebSocket daemon at addr exposing space's chats until ctx is cancelled. Every /ws
// connection must carry a paired device's bearer; the first device pairs via POST /pair with the
// bootstrap code logged at startup, further devices via POST /join + /join/confirm. One backend,
// many fronts — the backend is the truth.
func Serve(ctx context.Context, addr string, spaces map[string]*workspace.Workspace, devices *auth.Store, broker *hitl.Broker, voices *speaker.Embedder, log *slog.Logger) error {
	return serveOn(ctx, addr, spaces, devices, broker, voices, log, defaultHeartbeat, nil)
}

// serveOn is Serve with the two things only a test varies: the liveness check its connections run,
// and a hook for the address it actually bound (so a test can ask for port 0 and still find the
// daemon). Production passes defaultHeartbeat and nil.
func serveOn(ctx context.Context, addr string, spaces map[string]*workspace.Workspace, devices *auth.Store, broker *hitl.Broker, voices *speaker.Embedder, log *slog.Logger, beat heartbeat, ready func(string)) error {
	log = log.With("component", "serve")
	if code := devices.Bootstrap(bootstrapTTL); code != "" {
		log.Info("no devices paired — pair one with POST /pair", "code", code, "validFor", bootstrapTTL)
	}

	// Every device is a sink of the whole daemon: chat activity (list changes) AND live chat events
	// (tokens/tools/turnEnd, tagged with chatId) are broadcast to all connections; the client routes
	// by chatId. So a session's turn is never tied to one connection.
	hub := newHub(beat)
	for name, ws := range spaces {
		ws.OnChatUpdate(func(m chat.Meta) {
			hub.broadcast(ChatActivity{Type: "chat.activity", Ws: name, Chat: m})
		})
		// Both managers' live events reach every device on the same wire form (chatEvent, tagged with
		// the chat/run id) — so an agent run streams its tokens/tools exactly like a user chat, and the
		// client renders it via the ONE reducer, distinguished only by id (agent runs list under kind
		// "agent"). The event carries no kind: the client already knows which ids are agent runs.
		stream := func(chatID string, ev agentkit.Event) {
			if msg, ok := chatEvent(chatID, ev); ok {
				hub.broadcast(msg)
			}
		}
		ws.Chats().OnEvent(stream)
		ws.AgentChats().OnEvent(stream)
		// A reminder set, cancelled or fired changes what a device should be listing. Broadcast the
		// bare fact and let each client re-list, so devices converge on the daemon's set.
		ws.OnReminderChange(func() {
			hub.broadcast(ReminderChanged{Type: "reminder.changed", Ws: name})
		})
		// The in-app half of a proactive delivery (a fired reminder or a notify): the notifier routes
		// here when a device is in the foreground, and to the push otherwise — so this fires only in
		// the former case, never both.
		ws.OnNotification(func(n tools.Notification) {
			hub.broadcast(Notification{
				Type: "notification", Ws: n.Ws, Kind: n.Kind,
				ChatID: n.ChatID, Title: n.Title, Message: n.Message,
			})
		})
		// On daemon shutdown (Serve returns): stop both managers' reapers and close every live session.
		defer ws.Close()
		// Start the cron schedulers only AFTER this workspace's subscriptions are wired: a scheduled
		// firing saves a transcript (→ OnSave/OnEvent), so starting earlier would race the wiring.
		go ws.StartAgents(ctx)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/pair", func(w http.ResponseWriter, r *http.Request) { handlePair(w, r, devices, log) })
	mux.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) { handleJoin(w, r, devices, hub, log) })
	mux.HandleFunc("/join/confirm", func(w http.ResponseWriter, r *http.Request) { handleJoinConfirm(w, r, devices, log) })
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) { handleRegister(w, r, devices, log) })
	mux.HandleFunc("POST /devices", func(w http.ResponseWriter, r *http.Request) { handleEnrol(w, r, devices, log) })
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"}, // dev: the companion app's origin is not fixed
		})
		if err != nil {
			log.Warn("ws accept failed", "err", err, "remote", r.RemoteAddr)
			return
		}
		defer ws.CloseNow()

		// Verify AFTER the upgrade, then close with app code 4401 on a bad bearer: a pre-upgrade
		// HTTP 401 is invisible to the browser WebSocket API, so the client couldn't tell auth
		// failure from a network drop and would reconnect-loop instead of re-pairing.
		bearer := bearerOf(r)
		dev, ok := devices.Lookup(bearer)
		if !ok {
			log.Warn("ws unauthorized", "remote", r.RemoteAddr)
			_ = ws.Close(4401, "unauthorized")
			return
		}
		devices.UpdateLastUsed(bearer)
		// The one place a class becomes authority for a connection: a device that may not approve is
		// handed no broker at all, so there is nothing for it to answer with.
		can := capabilitiesOf(dev.Class)
		approver := broker
		if !can.approve {
			approver = nil
		}
		newConn(ws, spaces, devices, approver, hub, dev.ID, can, voices,
			log.With("remote", r.RemoteAddr, "device", dev.ID, "class", dev.Class)).serve(r.Context())
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	// The bound address, not the requested one: with port 0 they differ, which is what lets a test
	// take a free port and still know where to connect.
	addr = ln.Addr().String()
	if ready != nil {
		ready(addr)
	}

	srv := &http.Server{Handler: cors(mux)}
	// Graceful shutdown on cancel; stop() cancels the hook if ListenAndServe returns first, so the
	// watcher never leaks.
	stop := context.AfterFunc(ctx, func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	})
	defer stop()

	// Advertise on the LAN so the app finds the daemon without a typed IP. Only meaningful off
	// loopback; a failure is non-fatal (the app can still connect by IP).
	if !isLoopbackAddr(addr) {
		if shutdown, err := advertiseMDNS(addr); err != nil {
			log.Warn("mdns advertise failed — the app must connect by IP", "err", err)
		} else {
			log.Info("discoverable on the LAN", "service", mdnsService)
			defer shutdown()
		}
	}

	log.Info("ws daemon listening", "addr", addr)
	return srv.Serve(ln)
}
