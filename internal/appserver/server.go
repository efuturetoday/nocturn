package appserver

import (
	"context"
	"sync"
)

// Server is the companion-app protocol server: it multiplexes, over ONE client Conn, a
// CONTROL plane (list/inspect/switch workspaces, edit config) and one active CHAT (the
// open workspace's live turn stream). It holds no filesystem access — it consumes the
// Workspaces state service, so the state is the contract and the loaders stay hidden.
//
// A remote client is no more powerful than the local TUI: chat commands route through the
// workspace's Runner, so every effect still passes the broker + HITL, and authority-
// granting config changes (installing a plugin, connecting an account) still go through
// the out-of-band review — they are not raw commands here.
type Server struct {
	workspaces Workspaces
}

// NewServer builds a server over the workspace state service. Call Handle per connection.
func NewServer(ws Workspaces) *Server { return &Server{workspaces: ws} }

// Handle serves one client connection until ctx is cancelled, the client disconnects, or
// a write fails. It runs a single writer (all outbound funnels through one channel, so the
// Conn only ever sees one concurrent writer), a single reader (commands), and — while a
// workspace is open — one chat pump streaming that workspace's Runner. The caller closes
// the conn.
func (s *Server) Handle(ctx context.Context, conn Conn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := make(chan []byte, 64)
	errc := make(chan error, 2)

	// The ONLY writer: everything outbound (control replies + chat events) funnels here.
	go func() {
		for {
			select {
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			case msg := <-out:
				if err := conn.Write(ctx, msg); err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	h := &clientConn{workspaces: s.workspaces, out: out}
	defer h.closeChat()

	// The ONLY reader: commands in.
	go func() {
		for {
			msg, err := conn.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			h.dispatch(ctx, msg)
		}
	}()

	return <-errc
}

// clientConn is the per-connection state: which workspace's chat is open (its Runner and
// the cancel for its pump), plus the shared outbound channel.
type clientConn struct {
	workspaces Workspaces
	out        chan []byte

	mu         sync.Mutex
	runner     Runner             // the open workspace's turn loop, or nil
	chatCancel context.CancelFunc // stops the current chat pump, or nil
}

// dispatch routes one client command: control commands act on the Workspaces service and
// reply on the stream; chat commands route to the OPEN workspace's Runner (ignored if no
// workspace is open). A malformed/unknown command is dropped, never fatal.
func (h *clientConn) dispatch(ctx context.Context, msg []byte) {
	c, err := decodeCommand(msg)
	if err != nil {
		return
	}
	switch c.Cmd {
	case "listWorkspaces":
		h.send(encodeWorkspaces(h.workspaces.List()))
	case "getWorkspace":
		if st, ok := h.workspaces.Get(c.Name); ok {
			h.send(encodeWorkspace(st))
		} else {
			h.send(encodeError("unknown workspace: " + c.Name))
		}
	case "openWorkspace":
		h.openWorkspace(ctx, c.Name)
	case "setPersona":
		if err := h.workspaces.SetPersona(c.Name, c.Text); err != nil {
			h.send(encodeError("set persona: " + err.Error()))
			return
		}
		if st, ok := h.workspaces.Get(c.Name); ok {
			h.send(encodeWorkspace(st)) // echo the new state back
		}
	default:
		// A chat command — route to the open workspace's Runner.
		h.mu.Lock()
		r := h.runner
		h.mu.Unlock()
		if r != nil {
			routeChatCommand(r, c)
		}
	}
}

// openWorkspace switches the active chat: it stops the current pump, resolves the named
// workspace's Runner, and starts a fresh pump that sends a snapshot then streams events.
func (h *clientConn) openWorkspace(ctx context.Context, name string) {
	r, ok := h.workspaces.Open(name)
	if !ok {
		h.send(encodeError("unknown workspace: " + name))
		return
	}
	h.mu.Lock()
	if h.chatCancel != nil {
		h.chatCancel() // stop the previous workspace's pump
	}
	cctx, cancel := context.WithCancel(ctx)
	h.runner, h.chatCancel = r, cancel
	h.mu.Unlock()

	go runChat(cctx, r, h.out)
}

// closeChat stops any active chat pump (on disconnect).
func (h *clientConn) closeChat() {
	h.mu.Lock()
	if h.chatCancel != nil {
		h.chatCancel()
		h.chatCancel = nil
	}
	h.mu.Unlock()
}

// send funnels one outbound message to the writer; it drops the message if the connection
// is going away, so a control reply can never block the reader on a dead conn.
func (h *clientConn) send(msg []byte) {
	select {
	case h.out <- msg:
	default:
		// buffer full or writer gone: drop — the client re-syncs via listWorkspaces/snapshot
	}
}

// runChat streams one Runner to the outbound channel: a snapshot first (so the client
// renders current state on open/switch), then every event until ctx is cancelled (the
// pump is replaced on a workspace switch) or the stream closes. It is pump-only — chat
// commands come through the server's reader and route to the Runner directly.
func runChat(ctx context.Context, r Runner, out chan<- []byte) {
	sub, unsub := r.Subscribe()
	defer unsub()

	if snap, err := EncodeSnapshot(r.Snapshot()); err == nil {
		select {
		case out <- snap:
		case <-ctx.Done():
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub:
			if !ok {
				return
			}
			msg, err := EncodeEvent(e)
			if err != nil {
				continue // an event we don't encode: skip, never kill the stream
			}
			select {
			case out <- msg:
			case <-ctx.Done():
				return
			}
		}
	}
}
