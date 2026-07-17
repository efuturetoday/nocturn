package appserver_test

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/appserver"
	"github.com/efuturetoday/nocturn/internal/session"
)

// fakeRunner is a test double for the Bridge's Runner: it exposes a controllable event
// channel and a fixed snapshot, and records the commands the Bridge dispatched.
type fakeRunner struct {
	events chan session.Event
	snap   session.Snapshot

	mu       sync.Mutex
	submits  []string
	resolves []resolveCall
}

type resolveCall struct {
	id     string
	choice int
}

func (f *fakeRunner) Subscribe() (<-chan session.Event, func()) { return f.events, func() {} }
func (f *fakeRunner) Snapshot() session.Snapshot                { return f.snap }
func (f *fakeRunner) Submit(_ session.Source, input string) {
	f.mu.Lock()
	f.submits = append(f.submits, input)
	f.mu.Unlock()
}
func (f *fakeRunner) SubmitInput(_ session.Source, _, _ string) {}
func (f *fakeRunner) SubmitAgent(_, _, _ string)                {}
func (f *fakeRunner) Cancel()                                   {}
func (f *fakeRunner) Reset()                                    {}
func (f *fakeRunner) Resolve(id string, choice int) {
	f.mu.Lock()
	f.resolves = append(f.resolves, resolveCall{id, choice})
	f.mu.Unlock()
}
func (f *fakeRunner) gotSubmit(input string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.submits, input)
}
func (f *fakeRunner) gotResolve(id string, choice int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.resolves {
		if r.id == id && r.choice == choice {
			return true
		}
	}
	return false
}

// fakeConn scripts client→server messages (in) and captures server→client messages (out).
type fakeConn struct {
	in     chan []byte
	out    chan []byte
	closed chan struct{}
	once   sync.Once
}

func (c *fakeConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case m := <-c.in:
		return m, nil
	case <-c.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (c *fakeConn) Write(ctx context.Context, msg []byte) error {
	select {
	case c.out <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func recvMsg(t *testing.T, out <-chan []byte) map[string]any {
	t.Helper()
	select {
	case m := <-out:
		var v map[string]any
		if err := json.Unmarshal(m, &v); err != nil {
			t.Fatalf("bad json written to conn: %v (%s)", err, m)
		}
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a message on the conn")
		return nil
	}
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// The Bridge sends a snapshot first, forwards Runner events as tagged JSON, and dispatches
// client commands to the Runner — the whole app protocol in one round trip.
func TestBridge_SnapshotEventsAndCommands(t *testing.T) {
	fr := &fakeRunner{events: make(chan session.Event, 4), snap: session.Snapshot{Running: true}}
	fc := &fakeConn{in: make(chan []byte, 4), out: make(chan []byte, 8), closed: make(chan struct{})}
	b := appserver.NewBridge(fr, fc)

	go func() { _ = b.Serve(t.Context()) }()

	// 1. the first message is the snapshot with the running flag.
	if snap := recvMsg(t, fc.out); snap["type"] != "snapshot" || snap["running"] != true {
		t.Fatalf("first message = %v, want a snapshot with running:true", snap)
	}

	// 2. a Runner event is encoded and forwarded.
	fr.events <- session.TokenEvent{Text: "hi"}
	if tok := recvMsg(t, fc.out); tok["type"] != "token" || tok["text"] != "hi" {
		t.Fatalf("event = %v, want token(hi)", tok)
	}

	// 3. an approval event carries its id + options.
	fr.events <- session.ApprovalEvent{ID: "appr-1", Intent: "Send email", Options: []string{"Allow once", "Deny"}}
	if ap := recvMsg(t, fc.out); ap["type"] != "approval" || ap["id"] != "appr-1" {
		t.Fatalf("event = %v, want approval(appr-1)", ap)
	}

	// 4. a submit command reaches the Runner.
	fc.in <- []byte(`{"cmd":"submit","input":"hello"}`)
	eventually(t, func() bool { return fr.gotSubmit("hello") }, "submit command did not reach the runner")

	// 5. a resolve command (approve choice 0) reaches the Runner.
	fc.in <- []byte(`{"cmd":"resolve","id":"appr-1","choice":0}`)
	eventually(t, func() bool { return fr.gotResolve("appr-1", 0) }, "resolve command did not reach the runner")
}

// A malformed or unknown command is ignored, not fatal — the session stays alive.
func TestBridge_BadCommandKeepsSessionAlive(t *testing.T) {
	fr := &fakeRunner{events: make(chan session.Event, 4)}
	fc := &fakeConn{in: make(chan []byte, 4), out: make(chan []byte, 8), closed: make(chan struct{})}
	b := appserver.NewBridge(fr, fc)

	go func() { _ = b.Serve(t.Context()) }()

	recvMsg(t, fc.out) // drain the snapshot

	fc.in <- []byte(`not json`)             // malformed
	fc.in <- []byte(`{"cmd":"frobnicate"}`) // unknown
	fc.in <- []byte(`{"cmd":"submit","input":"still here"}`)
	eventually(t, func() bool { return fr.gotSubmit("still here") }, "session died on a bad command")
}
