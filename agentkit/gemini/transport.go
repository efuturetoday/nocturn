package gemini

import "context"

// Transport is the WebSocket this adapter speaks over, injected rather than imported. That keeps
// this module dependency-free (agentkit's rule: the engine and its siblings carry no third-party
// deps that would follow it into its own repository) and makes the whole protocol testable against
// a scripted fake — no network, no API key, no flakiness in `go test ./...`.
//
// An implementation must be safe for one concurrent Send and one concurrent Recv, which is what the
// adapter does: a writer goroutine and a reader goroutine. It need NOT support concurrent Sends.
type Transport interface {
	// Send writes one complete text frame.
	Send(ctx context.Context, frame []byte) error
	// Recv blocks for the next complete text frame. It returns an error once the peer closes.
	Recv(ctx context.Context) ([]byte, error)
	// Close releases the connection. It must be safe to call more than once.
	Close() error
}

// Dialer opens a Transport to url. nocturn supplies a coder/websocket-backed one; tests supply a
// scripted fake.
type Dialer func(ctx context.Context, url string) (Transport, error)
