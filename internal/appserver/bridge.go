package appserver

import (
	"context"
	"errors"

	"github.com/efuturetoday/nocturn/internal/session"
)

// Conn is one duplex message stream to a client — FRAMED []byte messages, not a raw byte
// pipe. The daemon adapts a WebSocket to it; a future relay is just a different Conn, so
// the Bridge never learns which transport it is on.
//
// Read and Write MUST honor ctx cancellation (return promptly when ctx is done) — the
// Bridge cancels ctx to stop its reader/writer goroutines, so a Conn that ignores ctx
// would leak the command-reader goroutine on disconnect.
type Conn interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, msg []byte) error
	Close() error
}

// Runner is the subset of *session.Runner the Bridge drives — commands in, events +
// snapshot out. *session.Runner satisfies it structurally (asserted in the daemon
// wiring); the interface is here so the Bridge is testable with a fake.
type Runner interface {
	Submit(source session.Source, input string)
	SubmitInput(source session.Source, display, input string)
	SubmitAgent(display, name, task string)
	Cancel()
	Reset()
	Resolve(id string, choice int)
	Subscribe() (<-chan session.Event, func())
	Snapshot() session.Snapshot
}

// The production Runner is *session.Runner — proven here so the interface can never
// drift from what the daemon actually wires.
var _ Runner = (*session.Runner)(nil)

// Bridge connects one Runner to one client Conn: a single reader goroutine (commands in)
// and a single writer (snapshot + events out), so the Conn only ever sees one concurrent
// reader and one concurrent writer — the constraint every WebSocket library imposes.
type Bridge struct {
	runner Runner
	conn   Conn
}

// NewBridge bridges r to c. Call Serve to run it.
func NewBridge(r Runner, c Conn) *Bridge { return &Bridge{runner: r, conn: c} }

// Serve runs until ctx is cancelled, the client disconnects, or a write fails. It
// subscribes to the Runner, sends a snapshot so the client renders current state, then
// pumps events out and commands in concurrently, returning the first terminal error. The
// caller closes the conn. Every command routes through the Runner, so the broker + HITL
// still gate every effect — a remote client has no more authority than the local TUI.
func (b *Bridge) Serve(ctx context.Context) error {
	// Subscribe BEFORE snapshotting so no event can slip through the gap between the two:
	// an event that fires meanwhile is buffered on the sub channel and delivered after the
	// snapshot. A duplicate (an id-keyed approval already in the snapshot) is harmless; a
	// gap would lose a live update.
	sub, unsub := b.runner.Subscribe()
	defer unsub()

	if snap, err := EncodeSnapshot(b.runner.Snapshot()); err == nil {
		if err := b.conn.Write(ctx, snap); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, 2)

	// Events out — the ONLY writer.
	go func() {
		for {
			select {
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			case e, ok := <-sub:
				if !ok {
					errc <- errors.New("appserver: event stream closed")
					return
				}
				msg, err := EncodeEvent(e)
				if err != nil {
					continue // an event we don't encode: skip, never kill the connection
				}
				if err := b.conn.Write(ctx, msg); err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	// Commands in — the ONLY reader.
	go func() {
		for {
			msg, err := b.conn.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			if err := dispatchCommand(b.runner, msg); err != nil {
				continue // a malformed/unknown command must not drop the session
			}
		}
	}()

	return <-errc
}
