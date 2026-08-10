package serve

import (
	"testing"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// Revocation is the exit from the one state that had none: a phone that is lost, sold or wiped kept
// a working bearer until someone edited devices.json by hand and restarted the daemon.
func TestDeviceForget_RevokesTheBearer(t *testing.T) {
	conn, ctx := gateDaemon(t, auth.ClassWeb)

	// A second device to lose. Its id comes off the roster, which is also the listing under test.
	send(t, conn, ctx, map[string]any{"cmd": "device.list"})
	before := awaitType(t, conn, ctx, "device.list")
	devices, _ := before["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("roster = %d devices, want the 1 this connection is", len(devices))
	}
	me, _ := devices[0].(map[string]any)
	id, _ := me["id"].(string)
	if id == "" {
		t.Fatalf("roster carried no id: %v", me)
	}
	// The daemon says which entry is us, rather than leaving the client to match on a name.
	if self, _ := before["self"].(string); self != id {
		t.Errorf("self = %q, want %q", self, id)
	}
	// The bearer's shadow never leaves the daemon — nothing outside auth has a use for it.
	if _, leaked := me["bearerHash"]; leaked {
		t.Error("the roster carries a bearer hash")
	}

	send(t, conn, ctx, map[string]any{"cmd": "device.forget", "id": id})
	after := awaitType(t, conn, ctx, "device.list")
	if got, _ := after["devices"].([]any); len(got) != 0 {
		t.Errorf("after forgetting the only device the roster still holds %d", len(got))
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
	conn, ctx, store := gateDaemonStore(t, auth.ClassWeb, OnDevicesChanged(func() {
		called++
		rosterWhenCalled = len(store.List())
	}))

	send(t, conn, ctx, map[string]any{"cmd": "device.list"})
	before := awaitType(t, conn, ctx, "device.list")
	devices, _ := before["devices"].([]any)
	me, _ := devices[0].(map[string]any)
	id, _ := me["id"].(string)

	send(t, conn, ctx, map[string]any{"cmd": "device.forget", "id": id})
	awaitType(t, conn, ctx, "device.list")

	if called != 1 {
		t.Fatalf("the registry hook ran %d times, want 1", called)
	}
	// Before the broadcast, not after: a roster that is missing a row and then grows one a moment
	// later reads as a bug rather than as repair.
	if rosterWhenCalled != 0 {
		t.Errorf("the hook saw %d devices, want 0 — it must run after the removal", rosterWhenCalled)
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
