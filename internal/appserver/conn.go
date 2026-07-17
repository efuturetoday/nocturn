package appserver

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/session"
)

// Conn is one duplex message stream to a client — FRAMED []byte messages, not a raw byte
// pipe. The daemon adapts a WebSocket to it; a future relay is just a different Conn, so
// the server never learns which transport it is on.
//
// Read and Write MUST honor ctx cancellation (return promptly when ctx is done) — the
// server cancels ctx to stop its reader/writer goroutines, so a Conn that ignores ctx
// would leak the command-reader goroutine on disconnect.
type Conn interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, msg []byte) error
	Close() error
}

// Runner is the subset of *session.Runner the chat pump drives — commands in, events +
// snapshot out. *session.Runner satisfies it structurally (asserted below); the interface
// is here so the server is testable with a fake.
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
