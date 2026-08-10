package serve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// mcpStates pulls (name, state) out of an mcp.list frame.
func mcpStates(msg map[string]any) map[string]string {
	out := map[string]string{}
	items, _ := msg["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		name, _ := m["name"].(string)
		state, _ := m["state"].(string)
		out[name] = state
	}
	return out
}

// Which servers a workspace declares, and which of them failed, is the same kind of fact as which
// workspaces exist — so listing is open. Declaring or dropping one takes manage.
func TestMCP_ListIsOpen_ChangesNeedManage(t *testing.T) {
	conn, ctx, _ := gateDaemonSpaces(t, auth.ClassAppliance)

	send(t, conn, ctx, map[string]any{"cmd": "mcp.list", "ws": workspace.DefaultWorkspace})
	if got := awaitType(t, conn, ctx, "mcp.list"); got["items"] == nil {
		t.Fatalf("an appliance could not list servers: %v", got)
	}

	for _, cmd := range []map[string]any{
		{"cmd": "mcp.add", "ws": workspace.DefaultWorkspace, "name": "acme", "url": "https://acme.example/mcp"},
		{"cmd": "mcp.remove", "ws": workspace.DefaultWorkspace, "name": "acme"},
		{"cmd": "mcp.reconnect", "ws": workspace.DefaultWorkspace},
	} {
		send(t, conn, ctx, cmd)
		if e := awaitType(t, conn, ctx, "error"); e["text"] == "" {
			t.Fatalf("%v was refused with no reason", cmd["cmd"])
		}
	}
}

// A server that is declared but not yet tried is CONNECTING. Reporting "failed" for a handshake
// nobody has run would be a lie; reporting nothing would leave a device staring at a list its own
// command is missing from.
func TestMCP_AddReportsConnectingThenTheOutcome(t *testing.T) {
	conn, ctx, spaces := gateDaemonSpaces(t, auth.ClassApp)
	ws, _ := spaces.Get(workspace.DefaultWorkspace)

	send(t, conn, ctx, map[string]any{
		"cmd": "mcp.add", "ws": workspace.DefaultWorkspace,
		"name": "acme", "url": "https://acme.invalid/mcp",
	})

	first := awaitType(t, conn, ctx, "mcp.list")
	if got := mcpStates(first)["acme"]; got != string(workspace.MCPConnecting) {
		t.Fatalf("the first list said %q, want %q", got, workspace.MCPConnecting)
	}

	// The declaration is on disk immediately — the disk is what the next start will read either way.
	if _, err := os.Stat(filepath.Join(ws.MCPDir(), "acme", "mcp.json")); err != nil {
		t.Fatalf("the declaration was not written: %v", err)
	}

	// The reload runs detached; the second list carries what actually happened. The host does not
	// resolve, so it is a failure — and a failure is KEPT in the list, with its reason.
	second := awaitType(t, conn, ctx, "mcp.list")
	if got := mcpStates(second)["acme"]; got == string(workspace.MCPConnecting) || got == "" {
		t.Fatalf("the second list still said %q — the outcome never arrived", got)
	}
}

// Removing drops the folder — the declaration and its secret shard together.
func TestMCP_RemoveDropsTheFolder(t *testing.T) {
	conn, ctx, spaces := gateDaemonSpaces(t, auth.ClassApp)
	ws, _ := spaces.Get(workspace.DefaultWorkspace)

	send(t, conn, ctx, map[string]any{
		"cmd": "mcp.add", "ws": workspace.DefaultWorkspace,
		"name": "acme", "url": "https://acme.invalid/mcp",
	})
	awaitType(t, conn, ctx, "mcp.list")
	awaitType(t, conn, ctx, "mcp.list") // the outcome, so the declaration is settled

	send(t, conn, ctx, map[string]any{
		"cmd": "mcp.remove", "ws": workspace.DefaultWorkspace, "name": "acme",
	})
	awaitType(t, conn, ctx, "mcp.list")

	if _, err := os.Stat(filepath.Join(ws.MCPDir(), "acme")); !os.IsNotExist(err) {
		t.Error("the folder survived the removal")
	}
	// That the host's remembered grant went with it is Workspace.ForgetNetAccess's own test — the
	// wire cannot see a grant, and inventing a way for it to would be API built for a test.
}
