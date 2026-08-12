package serve

import (
	"context"
	"encoding/json"
	"errors"
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

// dial brings up a daemon and returns one client socket for it.
func dial(t *testing.T) (*websocket.Conn, context.Context) {
	t.Helper()
	open, ctx := daemon(t)
	return open(), ctx
}

// daemon brings up a daemon with a live model configured and returns a way to open sockets
// authenticated as an appliance — the class a satellite has.
//
// More than one socket matters: a device that reconnects holds two for as long as the old one takes
// to time out, and they are the same device.
func daemon(t *testing.T) (func() *websocket.Conn, context.Context) {
	return daemonBeating(t, defaultHeartbeat)
}

// daemonBeating is daemon with the liveness check spelled out, for the tests that are about it.
func daemonBeating(t *testing.T, beat heartbeat) (func() *websocket.Conn, context.Context) {
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

	spaces, err := workspace.NewRegistry(
		workspace.Host{Live: &echoLive{sess: newEchoSession()}, Log: log},
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(spaces.Close)

	addr := serveTest(t, ctx, spaces, devices, log, beat)

	return func() *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?token="+bearer, nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return conn
	}, ctx
}

// A device that vanishes without a FIN — power cut, WiFi gone — leaves a connection indistinguishable
// from a quiet one: the read loop waits for bytes that will never arrive. The heartbeat is what tells
// them apart, and until it does, the device's voice session stays open and a live model bills for it.
//
// A client answers pings from inside its read path, so one that never reads never pongs — the same
// silence a device with a dead socket produces, and the reason this can be tested at all.
func TestConn_ASilentDeviceIsDisconnected(t *testing.T) {
	open, ctx := daemonBeating(t, fastHeartbeat)
	conn := open()

	time.Sleep(4 * (fastHeartbeat.every + fastHeartbeat.wait))

	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, _, err := conn.Read(deadline); err == nil {
		t.Fatal("a client that never answered a ping was still being served")
	} else if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("still connected after %v — the heartbeat never gave up", time.Since(start))
	}
}

// The other half, and the one that would break every satellite if it were wrong: a device that does
// answer must be left alone, however long it has nothing to say.
func TestConn_AnAnsweringDeviceIsKept(t *testing.T) {
	open, ctx := daemonBeating(t, fastHeartbeat)
	conn := open()

	// Reading is what answers the pings. Nothing is sent for the whole window: silence on the wire is
	// not what the heartbeat is looking for.
	dead := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				dead <- err
				return
			}
		}
	}()

	select {
	case err := <-dead:
		t.Fatalf("a connection that answered its pings was dropped: %v", err)
	case <-time.After(6 * (fastHeartbeat.every + fastHeartbeat.wait)):
	}
}

// fastHeartbeat is defaultHeartbeat at test speed. It belongs to the daemon a test starts, so nothing
// else in the binary beats at this rate.
var fastHeartbeat = heartbeat{every: 50 * time.Millisecond, wait: 50 * time.Millisecond}

// serveTest starts the daemon on a free port and returns its address.
func serveTest(
	t *testing.T,
	ctx context.Context,
	spaces *workspace.Registry,
	devices *auth.Store,
	log *slog.Logger,
	beat heartbeat,
	opts ...Option,
) string {
	t.Helper()
	broker := hitl.NewBroker(nil, log)
	ready := make(chan string, 1)
	go func() {
		_ = serveOn(ctx, "127.0.0.1:0", spaces, devices, broker, nil, log, beat, func(addr string) { ready <- addr }, opts...)
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
// credit stands in for a device's playout buffer. Without one nothing is sent: the downlink is
// credit-driven end to end, so a test that wakes a session and then waits for audio is testing the
// flow control as much as the audio.
func credit(t *testing.T, conn *websocket.Conn, ctx context.Context, bytes int) {
	t.Helper()
	send(t, conn, ctx, map[string]any{"cmd": "voice.credit", "ws": workspace.DefaultWorkspace, "bytes": bytes})
}

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
	credit(t, conn, ctx, 64000)

	// 300 samples at 16 kHz up; the echo returns them, converted from the model's 24 kHz for a board
	// that runs one clock, and delivered in 20 ms frames.
	if err := conn.Write(ctx, websocket.MessageBinary, pcm(make([]int16, 300))); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if got := len(awaitBinary(t, conn, ctx)) / 2; got == 0 || got > 320 {
		t.Errorf("frame of %d samples, want a 20 ms frame or less", got)
	}

	send(t, conn, ctx, map[string]string{"cmd": "voice.end", "ws": workspace.DefaultWorkspace})
	if got := awaitType(t, conn, ctx, "voice.state")["state"]; got != "idle" {
		t.Errorf("state after end = %v, want idle", got)
	}
}

// A session belongs to the device, not to the socket that opened it. A reconnecting device holds two
// connections until the dead one times out — and that is exactly when it says the wake word again,
// so the loser's teardown would otherwise cancel the conversation the winner had just started.
func TestVoiceWire_AStaleConnectionDoesNotEndTheLiveOnesSession(t *testing.T) {
	open, ctx := daemon(t)
	stale, live := open(), open()

	send(t, live, ctx, map[string]string{"cmd": "voice.wake", "ws": workspace.DefaultWorkspace})
	if got := awaitType(t, live, ctx, "voice.state")["state"]; got != "listening" {
		t.Fatalf("state = %v, want listening", got)
	}
	credit(t, live, ctx, 64000)

	// The reconnect's loser, finally timing out. Waiting is the only way to see its teardown from
	// out here: it produces nothing on the wire, and the connection it would wrongly end is this
	// side's, so there is no event to synchronise on — only the absence of a broken session after.
	stale.CloseNow()
	time.Sleep(200 * time.Millisecond)

	// The conversation must still work: it takes audio and answers it.
	if err := live.Write(ctx, websocket.MessageBinary, pcm(make([]int16, 300))); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if got := len(awaitBinary(t, live, ctx)); got == 0 {
		t.Error("no speech came back — the session is gone")
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
