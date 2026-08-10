package serve

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// writeSkillOn lays down a skill in the daemon's default workspace, as if a person had copied the
// folder in.
func writeSkillOn(t *testing.T, spaces *workspace.Registry, folder, name string) {
	t.Helper()
	ws, ok := spaces.Get(workspace.DefaultWorkspace)
	if !ok {
		t.Fatal("the registry has no default workspace")
	}
	dir := filepath.Join(ws.SkillsDir(), folder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: does a thing\n---\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// skillEntries pulls (name, enabled) out of a skill.list frame.
func skillEntries(msg map[string]any) map[string]bool {
	out := map[string]bool{}
	items, _ := msg["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		name, _ := m["name"].(string)
		on, _ := m["enabled"].(bool)
		out[name] = on
	}
	return out
}

// Reading a skill is ungated, the same call ADR-10 makes about skill_read: a skill is context, never
// authority. Changing the set is not — that takes `manage`, which an appliance does not have.
func TestSkill_ReadIsOpen_ChangesNeedManage(t *testing.T) {
	conn, ctx, spaces := gateDaemonSpaces(t, auth.ClassAppliance)
	writeSkillOn(t, spaces, "deploy", "deploy")

	send(t, conn, ctx, map[string]any{"cmd": "skill.list", "ws": workspace.DefaultWorkspace})
	got := awaitType(t, conn, ctx, "skill.list")
	if entries := skillEntries(got); !entries["deploy"] {
		t.Fatalf("an appliance could not list skills: %v", got)
	}

	send(t, conn, ctx, map[string]any{"cmd": "skill.read", "ws": workspace.DefaultWorkspace, "name": "deploy"})
	if body := awaitType(t, conn, ctx, "skill.body"); body["body"] == "" {
		t.Fatalf("an appliance could not read a skill: %v", body)
	}

	for _, cmd := range []map[string]any{
		{"cmd": "skill.enable", "ws": workspace.DefaultWorkspace, "name": "deploy", "on": false},
		{"cmd": "skill.remove", "ws": workspace.DefaultWorkspace, "name": "deploy"},
	} {
		send(t, conn, ctx, cmd)
		if e := awaitType(t, conn, ctx, "error"); e["text"] == "" {
			t.Fatalf("%v was refused with no reason", cmd["cmd"])
		}
	}
}

// Switching a skill off takes it out of the catalog and leaves the folder in place, and the listing
// still shows it — a list that omitted it could not offer switching it back on.
func TestSkill_EnableTogglesTheCatalog(t *testing.T) {
	conn, ctx, spaces := gateDaemonSpaces(t, auth.ClassApp)
	writeSkillOn(t, spaces, "deploy", "deploy")
	ws, _ := spaces.Get(workspace.DefaultWorkspace)
	if err := ws.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := ws.Inventory().Skills; len(got) != 1 {
		t.Fatalf("skills before = %v, want [deploy]", got)
	}

	send(t, conn, ctx, map[string]any{
		"cmd": "skill.enable", "ws": workspace.DefaultWorkspace, "name": "deploy", "on": false,
	})
	got := awaitType(t, conn, ctx, "skill.list")
	if entries := skillEntries(got); entries["deploy"] {
		t.Fatalf("the disabled skill is still listed as enabled: %v", got)
	}

	// The daemon reloads detached, so wait for the catalog rather than assuming an instant.
	waitFor(t, func() bool { return len(ws.Inventory().Skills) == 0 })
	if _, err := os.Stat(filepath.Join(ws.SkillsDir(), ".disabled", "deploy")); err != nil {
		t.Errorf("the disabled skill's folder is not where it should be: %v", err)
	}

	send(t, conn, ctx, map[string]any{
		"cmd": "skill.enable", "ws": workspace.DefaultWorkspace, "name": "deploy", "on": true,
	})
	awaitType(t, conn, ctx, "skill.list")
	waitFor(t, func() bool { return len(ws.Inventory().Skills) == 1 })
}

// Removing takes the folder with it — no trash, unlike a workspace. A skill is instructions that came
// from somewhere and can come from there again; a workspace folder is conversations and a vault.
func TestSkill_RemoveDeletesTheFolder(t *testing.T) {
	conn, ctx, spaces := gateDaemonSpaces(t, auth.ClassApp)
	writeSkillOn(t, spaces, "deploy", "deploy")
	ws, _ := spaces.Get(workspace.DefaultWorkspace)

	send(t, conn, ctx, map[string]any{
		"cmd": "skill.remove", "ws": workspace.DefaultWorkspace, "name": "deploy",
	})
	got := awaitType(t, conn, ctx, "skill.list")
	if len(skillEntries(got)) != 0 {
		t.Fatalf("the removed skill is still listed: %v", got)
	}
	if _, err := os.Stat(filepath.Join(ws.SkillsDir(), "deploy")); !os.IsNotExist(err) {
		t.Error("the folder is still there")
	}
}

// gateDaemonSpaces is gateDaemon with the workspace registry handed back, for the tests that are
// about what happened on disk.
func gateDaemonSpaces(t *testing.T, class auth.Class) (*websocket.Conn, context.Context, *workspace.Registry) {
	t.Helper()
	conn, ctx, _, spaces := gateDaemonAll(t, class)
	return conn, ctx, spaces
}

// waitFor blocks until cond holds. The daemon reloads a workspace detached from the command that
// caused it — deliberately, so a reload's MCP handshakes cannot freeze the connection — so a test
// that asserted immediately would be asserting on the wrong moment.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("the workspace never reloaded")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
