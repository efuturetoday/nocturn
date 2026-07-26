package serve

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/workspace"
)

// testConn builds a connection wired only for the transport paths under test: a buffered out channel
// (mirroring newConn's cap 64), a discard logger, and empty maps. Fields a given test needs (broker,
// spaces) are set by the test itself.
func testConn() *conn {
	return &conn{
		spaces:  map[string]*workspace.Workspace{},
		hub:     newHub(),
		log:     slog.New(slog.DiscardHandler),
		control: make(chan any, 64),
	}
}

// recv takes one message off out, failing if none arrives — the handlers push synchronously to the
// buffered channel, so no writer goroutine is needed.
func recv(t *testing.T, c *conn) any {
	t.Helper()
	select {
	case msg := <-c.control:
		return msg
	case <-time.After(time.Second):
		t.Fatal("expected a message on out, got none")
		return nil
	}
}

// recvError takes one message and asserts it is an Error carrying want as a substring.
func recvError(t *testing.T, c *conn, want string) {
	t.Helper()
	msg := recv(t, c)
	e, ok := msg.(Error)
	if !ok {
		t.Fatalf("message = %T, want serve.Error", msg)
	}
	if e.Type != "error" {
		t.Errorf("Error.Type = %q, want %q", e.Type, "error")
	}
	if !contains(e.Text, want) {
		t.Errorf("Error.Text = %q, want to contain %q", e.Text, want)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestErrText(t *testing.T) {
	if got := errText(nil); got != "" {
		t.Errorf("errText(nil) = %q, want empty", got)
	}
	if got := errText(context.Canceled); got != context.Canceled.Error() {
		t.Errorf("errText(err) = %q, want %q", got, context.Canceled.Error())
	}
}

func TestNewError(t *testing.T) {
	e := newError("boom")
	if e.Type != "error" || e.Text != "boom" {
		t.Errorf("newError = %+v, want {error boom}", e)
	}
}

func TestConn_Send_EnqueuesOnBufferedChannel(t *testing.T) {
	c := testConn()
	c.send(context.Background(), "hi")
	if got := recv(t, c); got != "hi" {
		t.Errorf("received %v, want hi", got)
	}
}

// send must never block forever: when the writer can't take the message and ctx is already done, it
// returns instead of parking the producer.
func TestConn_Send_RespectsCtxCancel(t *testing.T) {
	c := &conn{log: slog.New(slog.DiscardHandler), control: make(chan any)} // unbuffered: send would block
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		c.send(ctx, "dropped")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("send did not return on a cancelled ctx — it blocked")
	}
	select {
	case msg := <-c.control:
		t.Fatalf("nothing should have been enqueued on a cancelled ctx, got %v", msg)
	default:
	}
}

// trySend drops silently when the buffer is full rather than blocking the broadcaster.
func TestConn_TrySend_DropsWhenFull(t *testing.T) {
	c := &conn{control: make(chan any, 1)}
	c.trySend("first")  // fills the buffer
	c.trySend("second") // dropped, must not block
	if got := <-c.control; got != "first" {
		t.Errorf("kept %v, want first (second must be dropped)", got)
	}
	select {
	case msg := <-c.control:
		t.Fatalf("buffer should be empty after the one kept message, got %v", msg)
	default:
	}
}

func TestDispatch_UnknownDomain_Error(t *testing.T) {
	c := testConn()
	c.dispatch(context.Background(), []byte(`{"cmd":"bogus.action"}`))
	recvError(t, c, "unknown domain")
}

func TestDispatch_BadCommandJSON_Error(t *testing.T) {
	c := testConn()
	c.dispatch(context.Background(), []byte(`not json`))
	recvError(t, c, "bad command")
}

func TestConn_Workspace_UnknownReturnsError(t *testing.T) {
	c := testConn()
	w, ok := c.workspace(context.Background(), "nope")
	if ok || w != nil {
		t.Fatalf("workspace(unknown) = (%v, %v), want (nil, false)", w, ok)
	}
	recvError(t, c, "unknown workspace")
}
