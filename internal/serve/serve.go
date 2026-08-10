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

	// devicesChanged is called after a connection modifies the device registry, or nil. Daemon-wide
	// like beat, and written once at construction — it is a fact about this daemon rather than about
	// any one connection, which is why it lives here and not on conn.
	devicesChanged func()

	mu    sync.Mutex
	conns map[*conn]struct{}
	seq   uint64
}

func newHub(beat heartbeat) *hub { return &hub{beat: beat, conns: map[*conn]struct{}{}} }

// notifyDevicesChanged runs the daemon's registry hook, if it has one.
//
// Synchronously and before the refreshed roster is broadcast, so what the hook does — re-minting the
// command line's credential — is already in the registry by the time any client sees the list. A
// roster missing a row that reappears a moment later reads as a bug.
func (h *hub) notifyDevicesChanged() {
	if h.devicesChanged != nil {
		h.devicesChanged()
	}
}

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

// countMatching reports how many live connections want accepts.
//
// It answers "is there anybody out there who could show this code", which /join needs in order to
// tell a joining device something true instead of leaving it to wait on a screen nobody will ever
// look at.
func (h *hub) countMatching(want func(*conn) bool) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for c := range h.conns {
		if want(c) {
			n++
		}
	}
	return n
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
func (h *hub) broadcast(msg any) { h.broadcastTo(func(*conn) bool { return true }, msg) }

// broadcastTo is broadcast narrowed to the connections want accepts.
//
// It exists because one message is not for everyone: a pending join carries the code that completes
// it, so it may only reach a connection allowed to enrol. The predicate takes the connection rather
// than a capability so that what a class may do stays read from conn.can, which serve.go computed
// once at accept time — there is no second place a class is interpreted.
func (h *hub) broadcastTo(want func(*conn) bool, msg any) {
	h.mu.Lock()
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		if want(c) {
			conns = append(conns, c)
		}
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.trySend(msg)
	}
}

// bootstrapTTL is how long a first-device pairing code stays valid.
const bootstrapTTL = 5 * time.Minute

// cors admits browser requests from the app's own origins and the page this daemon served, and
// refuses every other one outright. It answers the preflight OPTIONS with 204.
//
// It replaces an allow-all header, and the reason is worth keeping: these endpoints carry no cookies,
// which is normally what makes Access-Control-Allow-Origin: * harmless — the browser attaches no
// ambient authority, so the attacker's page learns nothing. That argument fails here, because the
// thing it would learn is the RESPONSE, and the response to /pair is a bearer. Any page the household
// visits could scan the LAN, find a daemon with a code armed, and grind /pair from the victim's own
// browser without ever being on the network.
//
// Refusing rather than merely withholding the header: a browser would block the read either way, but
// a 403 says so where a log can see it, and it stops the request before it reaches a handler that
// would otherwise spend a pairing attempt.
//
// This wraps the whole mux, so it is also the gate on the /ws upgrade — one rule, one place, nothing
// to drift.
func cors(h http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Which address this was sent TO, before which page sent it. Checking the Host first is not an
		// ordering preference: the Origin check compares Origin against Host, so it is only meaningful
		// once Host is known to be an address a stranger could not have pointed here (see hostOK).
		if !hostOK(r.Host) {
			log.Warn("request refused: this daemon is not addressed by that name",
				"host", r.Host, "path", r.URL.Path, "remote", r.RemoteAddr,
				"hint", "reach it by IP, localhost or its .local name, or set "+hostnamesEnv)
			http.Error(w, "unknown host — set "+hostnamesEnv+" to allow this name", http.StatusForbidden)
			return
		}
		if !originOK(r) {
			log.Warn("request refused: origin not allowed",
				"origin", r.Header.Get("Origin"), "path", r.URL.Path, "remote", r.RemoteAddr)
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		// Echo the caller's origin rather than "*". Vary tells any cache that the answer depends on it,
		// so one origin's 200 is never replayed to another.
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
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
func Serve(
	ctx context.Context,
	addr string,
	spaces *workspace.Registry,
	devices *auth.Store,
	broker *hitl.Broker,
	embedder *speaker.Embedder,
	log *slog.Logger,
	opts ...Option,
) error {
	return serveOn(ctx, addr, spaces, devices, broker, embedder, log, defaultHeartbeat, nil, opts...)
}

// serveOn is Serve with the two things only a test varies: the liveness check its connections run,
// and a hook for the address it actually bound (so a test can ask for port 0 and still find the
// daemon). Production passes defaultHeartbeat and nil.
func serveOn(
	ctx context.Context,
	addr string,
	spaces *workspace.Registry,
	devices *auth.Store,
	broker *hitl.Broker,
	embedder *speaker.Embedder,
	log *slog.Logger,
	beat heartbeat,
	ready func(string),
	opts ...Option,
) error {
	log = log.With("component", "serve")
	cfg := apply(opts)
	// Arm a bootstrap code only while nothing in the registry could bring a device in by itself.
	//
	// The test is a capability, not "is the registry empty", and the difference is the whole flow: the
	// daemon enrols its own command line (ClassTool) at startup, and an appliance may be enrolled on
	// someone's behalf. Neither can relay a join code, so counting them as "a device is paired" would
	// retire the bootstrap code while nothing was left that could pair the first phone — a household
	// nobody can ever enter. The decision lives here rather than in auth because it is a question
	// about what a class may DO, and capabilitiesOf is the one place that is known.
	if !householdCanEnrol(devices) {
		code := devices.ArmBootstrap(bootstrapTTL)
		// Name the way to get another one in the same breath as the code. This line scrolls past on a
		// busy server and the code behind it dies in five minutes; without the second half, missing it
		// once used to mean restarting the daemon.
		log.Info("nothing paired yet — enter this code in the app or the web UI; `nocturn pair` mints a new one any time",
			"code", code, "validFor", bootstrapTTL)
	}

	// Every device is a sink of the whole daemon: chat activity (list changes) AND live chat events
	// (tokens/tools/turnEnd, tagged with chatId) are broadcast to all connections; the client routes
	// by chatId. So a session's turn is never tied to one connection.
	hub := newHub(beat)
	hub.devicesChanged = cfg.onDevicesChanged

	// One place decides what the daemon does around a workspace, and the registry calls it for every
	// workspace there will ever be — the ones open at startup, and the ones a device creates later.
	// A startup loop could only do the first kind, which is how "created at runtime" would have
	// silently meant "streams nothing and runs no agents".
	spaces.OnOpen(func(ws *workspace.Workspace) { attach(ctx, hub, ws) })
	for _, ws := range spaces.Snapshot() {
		attach(ctx, hub, ws)
	}
	// On daemon shutdown (Serve returns): stop every workspace's reapers, timers and live sessions.
	// A workspace deleted before then was already closed by the registry.
	defer spaces.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/daemon.json", func(w http.ResponseWriter, r *http.Request) {
		handleDaemon(w, r, devices, cfg.version)
	})
	mux.HandleFunc("/pair", func(w http.ResponseWriter, r *http.Request) { handlePair(w, r, devices, log) })
	mux.HandleFunc("/pair/code", func(w http.ResponseWriter, r *http.Request) { handleArm(w, r, devices, log) })
	mux.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) { handleJoin(w, r, devices, hub, log) })
	mux.HandleFunc("/join/confirm", func(w http.ResponseWriter, r *http.Request) { handleJoinConfirm(w, r, devices, log) })
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) { handleRegister(w, r, devices, log) })
	mux.HandleFunc("POST /devices", func(w http.ResponseWriter, r *http.Request) { handleEnrol(w, r, devices, log) })
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// The Origin was already checked by cors, which wraps this mux — see originOK. Skipping the
			// library's own check is what keeps that ONE rule authoritative: coder/websocket compares
			// the Origin host to r.Host, which would be right for the served page and wrong for
			// capacitor://localhost, so leaving it on would mean two rules disagreeing about the phone.
			InsecureSkipVerify: true,
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
		newConn(ws, spaces, devices, approver, hub, dev.ID, can, embedder,
			log.With("remote", r.RemoteAddr, "device", dev.ID, "class", dev.Class)).serve(r.Context())
	})

	// The browser front-end, last and least specific. ServeMux matches the longest pattern, so every
	// route above still wins over this catch-all — registering it does not put assets in front of the
	// protocol, and a path the protocol does not claim is by definition one for the app's router.
	if cfg.webUI != nil {
		mux.Handle("/", cfg.webUI)
	}

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

	srv := &http.Server{Handler: cors(mux, log)}
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

// attach wires one workspace to the hub: chat activity, both event streams, reminder changes and
// proactive notifications, and then starts its background work.
//
// The order at the end is load-bearing. Schedulers start only AFTER the subscriptions are in place,
// because a scheduled firing saves a transcript and streams events — starting first would race the
// wiring and lose the first run of a workspace created a moment ago.
func attach(ctx context.Context, hub *hub, ws *workspace.Workspace) {
	name := ws.Name()
	ws.OnChatUpdate(func(m chat.Meta) {
		hub.broadcast(ChatActivity{Type: "chat.activity", Ws: name, Chat: m})
	})
	// Both managers' live events reach every device on the same wire form (chatEvent, tagged with the
	// chat/run id) — so an agent run streams its tokens/tools exactly like a user chat, and the client
	// renders it via the ONE reducer, distinguished only by id (agent runs list under kind "agent").
	// The event carries no kind: the client already knows which ids are agent runs.
	stream := func(chatID string, ev agentkit.Event) {
		if msg, ok := chatEvent(chatID, ev); ok {
			hub.broadcast(msg)
		}
	}
	ws.Chats().OnEvent(stream)
	ws.AgentChats().OnEvent(stream)
	// A reminder set, cancelled or fired changes what a device should be listing. Broadcast the bare
	// fact and let each client re-list, so devices converge on the daemon's set.
	ws.OnReminderChange(func() {
		hub.broadcast(ReminderChanged{Type: "reminder.changed", Ws: name})
	})
	// The in-app half of a proactive delivery (a fired reminder or a notify): the notifier routes here
	// when a device is in the foreground, and to the push otherwise — so this fires only in the former
	// case, never both.
	ws.OnNotification(func(n tools.Notification) {
		hub.broadcast(Notification{
			Type: "notification", Ws: n.Ws, Kind: n.Kind,
			ChatID: n.ChatID, Title: n.Title, Message: n.Message,
		})
	})
	go ws.StartAgents(ctx)
}
