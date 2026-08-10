package serve

import (
	"context"
	"log/slog"
	"slices"
	"strings"
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

	spaces, err := workspace.NewRegistry(workspace.Host{Log: log}, t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(spaces.Close)

	addr := serveTest(t, ctx, spaces, devices, log, defaultHeartbeat, opts...)
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

			spaces, err := workspace.NewRegistry(workspace.Host{Log: log}, t.TempDir())
			if err != nil {
				t.Fatalf("workspace: %v", err)
			}
			t.Cleanup(spaces.Close)

			serveTest(t, ctx, spaces, devices, log, defaultHeartbeat)

			if got := devices.BootstrapPending(); got != tc.wantCode {
				t.Errorf("with only a %s device present, bootstrap armed = %v, want %v", tc.class, got, tc.wantCode)
			}
		})
	}
}

// Changing the household's set of workspaces takes `manage`. Listing does not — which workspaces
// exist is context, and every paired device already names one on every chat command.
func TestWorkspaceCommands_RefusedWithoutManage(t *testing.T) {
	conn, ctx := gateDaemon(t, auth.ClassAppliance)

	for _, cmd := range []map[string]any{
		{"cmd": "workspace.create", "name": "work"},
		{"cmd": "workspace.rename", "name": workspace.DefaultWorkspace, "title": "Zuhause"},
		{"cmd": "workspace.delete", "name": "work"},
	} {
		send(t, conn, ctx, cmd)
		got := awaitType(t, conn, ctx, "error")
		if text, _ := got["text"].(string); text == "" {
			t.Fatalf("%v was refused with no reason: %v", cmd["cmd"], got)
		}
	}

	// Listing still works, and the refusals changed nothing.
	send(t, conn, ctx, map[string]any{"cmd": "workspace.list"})
	got := awaitType(t, conn, ctx, "workspace.list")
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("a refused create still changed the set: %v", got)
	}
}

// With manage, the three actions work end to end — and each broadcasts the new set rather than
// answering only the device that asked, so every attached device converges on it.
func TestWorkspaceCommands_CreateRenameDelete(t *testing.T) {
	for _, class := range []auth.Class{auth.ClassApp, auth.ClassWeb, auth.ClassTool} {
		t.Run(string(class), func(t *testing.T) {
			conn, ctx := gateDaemon(t, class)

			send(t, conn, ctx, map[string]any{"cmd": "workspace.create", "name": "work", "title": "Arbeit"})
			got := awaitType(t, conn, ctx, "workspace.list")
			if names := workspaceNames(got); !slices.Equal(names, []string{workspace.DefaultWorkspace, "work"}) {
				t.Fatalf("after create, names = %v", names)
			}
			if title := workspaceTitle(got, "work"); title != "Arbeit" {
				t.Fatalf("create did not set the title in the same breath: %q", title)
			}

			// The title is a label. The name it was created under is still its identity.
			send(t, conn, ctx, map[string]any{"cmd": "workspace.rename", "name": "work", "title": "Büro"})
			got = awaitType(t, conn, ctx, "workspace.list")
			if title := workspaceTitle(got, "work"); title != "Büro" {
				t.Fatalf("after rename, title = %q, want \"Büro\"", title)
			}
			if names := workspaceNames(got); !slices.Equal(names, []string{workspace.DefaultWorkspace, "work"}) {
				t.Fatalf("rename moved the identity: %v", names)
			}

			send(t, conn, ctx, map[string]any{"cmd": "workspace.delete", "name": "work"})
			got = awaitType(t, conn, ctx, "workspace.list")
			if names := workspaceNames(got); !slices.Equal(names, []string{workspace.DefaultWorkspace}) {
				t.Fatalf("after delete, names = %v", names)
			}
		})
	}
}

// The default workspace is recreated at startup, so deleting it would appear to work and then undo
// itself. Refusing is the honest answer.
func TestWorkspaceDelete_RefusesTheDefault(t *testing.T) {
	conn, ctx := gateDaemon(t, auth.ClassApp)

	send(t, conn, ctx, map[string]any{"cmd": "workspace.delete", "name": workspace.DefaultWorkspace})
	got := awaitType(t, conn, ctx, "error")
	if text, _ := got["text"].(string); !strings.Contains(text, "default") {
		t.Fatalf("the refusal did not say why: %q", text)
	}
}

// A name becomes a directory and the input to this workspace's vault key, so it answers to the same
// rule plugins, MCP servers and agents do — not to whatever a client typed.
func TestWorkspaceCreate_RejectsAnInvalidName(t *testing.T) {
	conn, ctx := gateDaemon(t, auth.ClassApp)

	for _, name := range []string{"", "../escape", "Work", "with space", ".hidden"} {
		send(t, conn, ctx, map[string]any{"cmd": "workspace.create", "name": name})
		got := awaitType(t, conn, ctx, "error")
		if text, _ := got["text"].(string); text == "" {
			t.Fatalf("name %q was refused with no reason", name)
		}
	}
}

// workspaceNames pulls the sorted names out of a workspace.list frame.
func workspaceNames(msg map[string]any) []string {
	items, _ := msg["items"].([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		name, _ := m["name"].(string)
		out = append(out, name)
	}
	return out
}

// workspaceTitle pulls one workspace's display title out of a workspace.list frame.
func workspaceTitle(msg map[string]any, name string) string {
	items, _ := msg["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if n, _ := m["name"].(string); n == name {
			title, _ := m["title"].(string)
			return title
		}
	}
	return ""
}
