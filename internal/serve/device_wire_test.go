package serve

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// household brings up a daemon with an admin socket and a second device that can be revoked from it.
//
// Two devices rather than one, because revocation is only observable against a device that is not the
// caller: forgetting your own row and then reading the roster on the socket that row admitted tests
// the listing and nothing else.
func twoDevices(t *testing.T, opts ...Option) (
	admin *websocket.Conn, ctx context.Context, devices *auth.Store, addr, targetBearer string,
) {
	t.Helper()
	ctx = t.Context()
	log := slog.New(slog.DiscardHandler)

	devices, err := auth.New(t.TempDir() + "/devices.json")
	if err != nil {
		t.Fatalf("device store: %v", err)
	}
	adminBearer, err := devices.Mint("admin", auth.ClassWeb)
	if err != nil {
		t.Fatalf("mint admin: %v", err)
	}
	targetBearer, err = devices.Mint("target", auth.ClassApp)
	if err != nil {
		t.Fatalf("mint target: %v", err)
	}

	spaces, err := workspace.NewRegistry(workspace.Host{Log: log}, t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(spaces.Close)

	addr = serveTest(t, ctx, spaces, devices, log, defaultHeartbeat, opts...)
	admin = dialAs(t, ctx, addr, adminBearer)
	return admin, ctx, devices, addr, targetBearer
}

// dialAs opens one authenticated socket.
func dialAs(t *testing.T, ctx context.Context, addr, bearer string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?token="+bearer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// idOf finds a device's id on the roster by name.
func idOf(t *testing.T, roster map[string]any, name string) string {
	t.Helper()
	devices, _ := roster["devices"].([]any)
	for _, raw := range devices {
		d, _ := raw.(map[string]any)
		if n, _ := d["name"].(string); n == name {
			id, _ := d["id"].(string)
			if id == "" {
				t.Fatalf("device %q carried no id: %v", name, d)
			}
			// The bearer's shadow never leaves the daemon — nothing outside auth has a use for it.
			if _, leaked := d["bearerHash"]; leaked {
				t.Error("the roster carries a bearer hash")
			}
			return id
		}
	}
	t.Fatalf("no device named %q on the roster: %v", name, roster)
	return ""
}

// Revocation is the exit from the one state that had none: a phone that is lost, sold or wiped kept a
// working bearer until someone edited devices.json by hand and restarted the daemon.
//
// And revoking it has to reach the socket it already holds. A connection resolves its identity and
// its capabilities once, at accept, so deleting the row alone leaves the lost phone able to go on
// sending commands and audio for as long as it keeps the connection open — which is the whole of the
// window an attacker with a stolen device would be in.
func TestDeviceForget_RevokesTheBearerAndTheLiveSession(t *testing.T) {
	admin, ctx, _, addr, targetBearer := twoDevices(t)
	target := dialAs(t, ctx, addr, targetBearer)

	// The target is a working connection before anything is revoked, so what follows cannot be read
	// as it never having worked.
	send(t, target, ctx, map[string]any{"cmd": "workspace.list"})
	awaitType(t, target, ctx, "workspace.list")

	send(t, admin, ctx, map[string]any{"cmd": "device.list"})
	before := awaitType(t, admin, ctx, "device.list")
	id := idOf(t, before, "target")
	// The daemon says which entry is us, rather than leaving the client to match on a name.
	if self, _ := before["self"].(string); self == "" || self == id {
		t.Errorf("self = %q, want the admin's own id and not the target's", self)
	}

	send(t, admin, ctx, map[string]any{"cmd": "device.forget", "id": id})
	after := awaitType(t, admin, ctx, "device.list")
	got, _ := after["devices"].([]any)
	if len(got) != 1 {
		t.Errorf("after forgetting one of two devices the roster holds %d, want 1", len(got))
	}

	// The live session is gone: the socket is closed, so the next command cannot even be written and
	// read back. Either half failing is the pass — a closed socket reports itself at whichever end of
	// the round trip notices first.
	probe, err := json.Marshal(map[string]any{"cmd": "workspace.list"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := target.Write(ctx, websocket.MessageText, probe); err == nil {
		deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if _, _, err = target.Read(deadline); err == nil {
			t.Error("the revoked device's connection still answers")
		}
	}

	// And it cannot come back with the bearer it had. The upgrade itself still succeeds — that is
	// deliberate, since a pre-upgrade 401 is invisible to the browser's WebSocket API — so what says
	// "refused" is the application close code, not a failed dial.
	again, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?token="+targetBearer, nil)
	if err != nil {
		t.Fatalf("dial with the revoked bearer: %v", err)
	}
	defer again.CloseNow()
	deadline2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel2()
	if _, _, err = again.Read(deadline2); websocket.CloseStatus(err) != 4401 {
		t.Errorf("reconnect with the revoked bearer: err = %v, want close 4401", err)
	}
}

// Forgetting a device tells the daemon, so it can repair what it owns before anyone is shown the new
// roster. Concretely: the daemon's own command-line credential is a device row like any other, and
// revoking it must ROTATE it rather than leave `nocturn pair` answering 401 until the next restart.
func TestDeviceForget_NotifiesTheDaemonBeforeAnnouncingTheRoster(t *testing.T) {
	// The hook the daemon installs (cmd/nocturn passes ensureCLICredential). Recorded rather than
	// re-implemented: what it does is main's business, that it RAN — and when — is this package's.
	called := 0
	rosterWhenCalled := -1
	var store *auth.Store

	// The hook runs on the connection's own read-loop goroutine, and the assertions run after a
	// message that goes out from that same goroutine, so the send-then-read is the ordering edge —
	// no lock needed, and -race is what proves it.
	admin, ctx, store, _, _ := twoDevices(t, OnDevicesChanged(func() {
		called++
		rosterWhenCalled = len(store.List())
	}))

	send(t, admin, ctx, map[string]any{"cmd": "device.list"})
	before := awaitType(t, admin, ctx, "device.list")

	send(t, admin, ctx, map[string]any{"cmd": "device.forget", "id": idOf(t, before, "target")})
	awaitType(t, admin, ctx, "device.list")

	if called != 1 {
		t.Fatalf("the registry hook ran %d times, want 1", called)
	}
	// Before the broadcast, not after: a roster that is missing a row and then grows one a moment
	// later reads as a bug rather than as repair.
	if rosterWhenCalled != 1 {
		t.Errorf("the hook saw %d devices, want 1 — it must run after the removal", rosterWhenCalled)
	}
}

// Adding a device and removing one are the same authority pointed in opposite directions, so a
// holder that may not do the first has no business doing the second — and the roster is the shape of
// the household, which is not a stranger's to read either.
func TestDeviceCommands_RefusedWithoutEnrol(t *testing.T) {
	for _, class := range []auth.Class{auth.ClassAppliance, auth.ClassTool} {
		t.Run(string(class), func(t *testing.T) {
			conn, ctx := gateDaemon(t, class)
			for _, cmd := range []map[string]any{
				{"cmd": "device.list"},
				{"cmd": "device.forget", "id": "whatever"},
			} {
				send(t, conn, ctx, cmd)
				got := awaitType(t, conn, ctx, "error")
				if _, leaked := got["devices"]; leaked {
					t.Errorf("%v was refused but the answer carried the roster: %v", cmd, got)
				}
			}
		})
	}
}
