package serve

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// The gates a class opens, over a real socket. Both of these are reachable only by a connection whose
// class holds LESS than the companion app's, which is the case that acquired teeth the moment a
// second enrol-capable class existed.

// gateDaemon brings up a daemon and returns a socket authenticated as class.
func gateDaemon(t *testing.T, class auth.Class, opts ...Option) (*websocket.Conn, context.Context) {
	t.Helper()
	conn, ctx, _ := gateDaemonStore(t, class, opts...)
	return conn, ctx
}

// gateDaemonStore is gateDaemon with the device registry handed back, for the tests that are about
// what happened to it.
func gateDaemonStore(t *testing.T, class auth.Class, opts ...Option) (*websocket.Conn, context.Context, *auth.Store) {
	t.Helper()
	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)

	devices, err := auth.New(t.TempDir() + "/devices.json")
	if err != nil {
		t.Fatalf("device store: %v", err)
	}
	bearer, err := devices.Mint("holder", class)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	ws, err := workspace.Open(workspace.Host{Log: log}, workspace.DefaultWorkspace, t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(ws.Close)

	addr := serveTest(t, ctx, map[string]*workspace.Workspace{workspace.DefaultWorkspace: ws}, devices, log, defaultHeartbeat, opts...)
	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?token="+bearer, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn, ctx, devices
}

// A join code IS an enrolment: whoever reads one can complete the join and walk away with a device.
// A bearer alone must not reveal it, or an appliance in the hallway could multiply the household
// past the very subset test POST /devices applies.
func TestJoinList_RefusedWithoutEnrol(t *testing.T) {
	for _, class := range []auth.Class{auth.ClassAppliance, auth.ClassTool} {
		t.Run(string(class), func(t *testing.T) {
			conn, ctx := gateDaemon(t, class)

			send(t, conn, ctx, map[string]any{"cmd": "join.list"})
			got := awaitType(t, conn, ctx, "error")
			if text, _ := got["text"].(string); text == "" {
				t.Fatalf("error carried no text: %v", got)
			}
			if _, leaked := got["joins"]; leaked {
				t.Errorf("the refusal carried the join list anyway: %v", got)
			}
		})
	}
}

func TestJoinList_AllowedWithEnrol(t *testing.T) {
	for _, class := range []auth.Class{auth.ClassApp, auth.ClassWeb} {
		t.Run(string(class), func(t *testing.T) {
			conn, ctx := gateDaemon(t, class)

			send(t, conn, ctx, map[string]any{"cmd": "join.list"})
			got := awaitType(t, conn, ctx, "join.list")
			if _, ok := got["joins"]; !ok {
				t.Errorf("join.list answer carried no joins field: %v", got)
			}
		})
	}
}

// A device that may not approve is handed no broker at all — absence of authority rather than a
// check. presence.set then has nothing to record, and must say nothing rather than dereference it:
// SetActive locks through a pointer receiver, so the nil would panic the read loop and take the
// connection with it. The symptom is a satellite that silently drops the moment it reports
// foreground, which reads as a network fault.
func TestPresenceSet_SurvivesWithoutABroker(t *testing.T) {
	for _, class := range []auth.Class{auth.ClassAppliance, auth.ClassTool} {
		t.Run(string(class), func(t *testing.T) {
			conn, ctx := gateDaemon(t, class)

			send(t, conn, ctx, map[string]any{"cmd": "presence.set", "active": true})

			// The connection has to still be there afterwards. Ask it something it will answer, with a
			// deadline — a dead read loop shows up as a read error or a timeout, not as a wrong answer.
			send(t, conn, ctx, map[string]any{"cmd": "workspace.list"})
			deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if _, _, err := conn.Read(deadline); err != nil {
				t.Fatalf("the connection died after presence.set: %v", err)
			}
		})
	}
}

// The bootstrap decision moved out of internal/auth and into serveOn, where a class can be turned
// into a capability. These two cases are why the test is on a capability and not on "is the registry
// empty": the daemon enrols its own command line at startup, and neither it nor an appliance can
// relay a join code — so counting them would retire the only code that could pair a first phone.
func TestServeOn_ArmsTheBootstrapUnlessSomethingCanEnrol(t *testing.T) {
	for _, tc := range []struct {
		class    auth.Class
		wantCode bool
	}{
		{auth.ClassTool, true},
		{auth.ClassAppliance, true},
		{auth.ClassApp, false},
		{auth.ClassWeb, false},
	} {
		t.Run(string(tc.class), func(t *testing.T) {
			ctx := t.Context()
			log := slog.New(slog.DiscardHandler)

			devices, err := auth.New(t.TempDir() + "/devices.json")
			if err != nil {
				t.Fatalf("device store: %v", err)
			}
			if _, err := devices.Mint("existing", tc.class); err != nil {
				t.Fatalf("mint: %v", err)
			}

			ws, err := workspace.Open(workspace.Host{Log: log}, workspace.DefaultWorkspace, t.TempDir())
			if err != nil {
				t.Fatalf("workspace: %v", err)
			}
			t.Cleanup(ws.Close)

			serveTest(t, ctx, map[string]*workspace.Workspace{workspace.DefaultWorkspace: ws}, devices, log, defaultHeartbeat)

			if got := devices.BootstrapPending(); got != tc.wantCode {
				t.Errorf("with only a %s device present, bootstrap armed = %v, want %v", tc.class, got, tc.wantCode)
			}
		})
	}
}
