package serve

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// The wire end to end, over a real socket: wake, stream microphone audio, hear it come back, hang up.
//
// The pieces below it are covered on their own — the manager against a scripted live model, the
// resampler against a reference tone — but nothing until now exercised the joins between the read
// loop, the hub and the manager, which is precisely where a routing mistake would live. No board and
// no provider are involved; the live model is a fake that echoes what it is given.

// echoLive is a live model that plays back whatever audio it receives, which is all this test needs
// a model to do.
type echoLive struct{ sess *echoSession }

func (e *echoLive) Open(_ context.Context, _ []agentkit.Message, _ []agentkit.ToolSpec) (agentkit.LiveSession, error) {
	return e.sess, nil
}

type echoSession struct {
	events chan agentkit.LiveEvent
	closed chan struct{}
}

func newEchoSession() *echoSession {
	return &echoSession{events: make(chan agentkit.LiveEvent, 64), closed: make(chan struct{})}
}

// SendAudio turns the uplink straight around. The daemon downsamples 24 kHz to 16 kHz on the way
// out, so what comes back is two thirds the length — which is itself worth asserting.
func (s *echoSession) SendAudio(_ context.Context, pcm []byte) error {
	select {
	case s.events <- agentkit.LiveAudio{PCM: pcm}:
	case <-s.closed:
	}
	return nil
}

func (s *echoSession) SendResult(context.Context, agentkit.ToolResult) error { return nil }
func (s *echoSession) Events() <-chan agentkit.LiveEvent                     { return s.events }

func (s *echoSession) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

// dial brings up a daemon with a live model configured and returns a client socket authenticated as
// an appliance — the class a satellite has.
func dial(t *testing.T) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)

	devices, err := auth.New(t.TempDir() + "/devices.json")
	if err != nil {
		t.Fatalf("device store: %v", err)
	}
	bearer, err := devices.Mint("hallway", auth.ClassAppliance)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	ws, err := workspace.Open(
		workspace.Host{Live: &echoLive{sess: newEchoSession()}, Log: log},
		workspace.DefaultWorkspace, t.TempDir(),
	)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(ws.Close)

	addr := serveTest(t, ctx, map[string]*workspace.Workspace{workspace.DefaultWorkspace: ws}, devices, log)

	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?token="+bearer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn, ctx
}

// serveTest starts the daemon on a free port and returns its address.
func serveTest(t *testing.T, ctx context.Context, spaces map[string]*workspace.Workspace, devices *auth.Store, log *slog.Logger) string {
	t.Helper()
	broker := hitl.NewBroker(nil, log)
	ready := make(chan string, 1)
	go func() {
		_ = serveOn(ctx, "127.0.0.1:0", spaces, devices, broker, log, func(addr string) { ready <- addr })
	}()
	select {
	case addr := <-ready:
		return addr
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not start")
		return ""
	}
}

// send writes one tagged command.
func send(t *testing.T, conn *websocket.Conn, ctx context.Context, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// awaitBinary reads until a binary frame arrives, ignoring the control traffic in between.
func awaitBinary(t *testing.T, conn *websocket.Conn, ctx context.Context) []byte {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		typ, data, err := conn.Read(deadline)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if typ == websocket.MessageBinary {
			return data
		}
	}
}

// awaitType reads until a tagged message of the given type arrives.
func awaitType(t *testing.T, conn *websocket.Conn, ctx context.Context, want string) map[string]any {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		typ, data, err := conn.Read(deadline)
		if err != nil {
			t.Fatalf("read waiting for %s: %v", want, err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if m["type"] == want {
			return m
		}
	}
}

func TestVoiceWire_WakeStreamAndHearItBack(t *testing.T) {
	conn, ctx := dial(t)

	send(t, conn, ctx, map[string]string{"cmd": "voice.wake", "ws": workspace.DefaultWorkspace})
	if got := awaitType(t, conn, ctx, "voice.state")["state"]; got != "listening" {
		t.Fatalf("state = %v, want listening", got)
	}

	// 300 samples at 16 kHz up; 200 come back, because the model's 24 kHz output is converted for a
	// board that runs one clock.
	if err := conn.Write(ctx, websocket.MessageBinary, pcm(make([]int16, 300))); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if got, want := len(awaitBinary(t, conn, ctx))/2, 200; got != want {
		t.Errorf("got %d samples back, want %d", got, want)
	}

	send(t, conn, ctx, map[string]string{"cmd": "voice.end", "ws": workspace.DefaultWorkspace})
	if got := awaitType(t, conn, ctx, "voice.state")["state"]; got != "idle" {
		t.Errorf("state after end = %v, want idle", got)
	}
}

// Audio before a wake word is streamed into nothing. It must not error, and above all must not stall
// the read loop — a device may have frames in flight when a session is cancelled.
func TestVoiceWire_AudioWithoutASessionIsIgnored(t *testing.T) {
	conn, ctx := dial(t)

	if err := conn.Write(ctx, websocket.MessageBinary, pcm(make([]int16, 300))); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	// The connection is still serving: a command sent afterwards is answered.
	send(t, conn, ctx, map[string]string{"cmd": "voice.wake", "ws": workspace.DefaultWorkspace})
	awaitType(t, conn, ctx, "voice.state")
}

func TestVoiceWire_UnknownWorkspaceIsRejected(t *testing.T) {
	conn, ctx := dial(t)

	send(t, conn, ctx, map[string]string{"cmd": "voice.wake", "ws": "nowhere"})
	if got := awaitType(t, conn, ctx, "error")["text"]; got == nil {
		t.Error("no error returned for an unknown workspace")
	}
}
