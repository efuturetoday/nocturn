package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/library"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

const catalogSkill = "---\nname: deploy\ndescription: ships things\n---\n\nDo the deploy.\n"

func catalogBody(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(catalogSkill))
	b, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"version":       "2026-08-10",
		"skills": []map[string]any{{
			"id": "deploy", "title": "Deploy", "description": "ships things",
			"folder": "deploy", "body": catalogSkill, "sha256": hex.EncodeToString(sum[:]),
		}},
		"mcp": []map[string]any{{
			"id": "acme", "title": "Acme", "description": "an example server",
			"name": "acme", "url": "https://acme.invalid/mcp",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// withCatalog is a serve Option serving the test catalog.
func withCatalog(t *testing.T) Option {
	t.Helper()
	body := catalogBody(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return WithLibrary(library.New(library.Source{URL: srv.URL}, t.TempDir(), slog.New(slog.DiscardHandler)))
}

// Browsing grants nothing, so it is open; installing changes what a workspace is made of, so it takes
// manage.
func TestLibrary_BrowseIsOpen_InstallNeedsManage(t *testing.T) {
	conn, ctx, _, _ := gateDaemonAll(t, auth.ClassAppliance, withCatalog(t))

	send(t, conn, ctx, map[string]any{"cmd": "library.list"})
	got := awaitType(t, conn, ctx, "library.catalog")
	skills, _ := got["skills"].([]any)
	if len(skills) != 1 {
		t.Fatalf("an appliance could not browse: %v", got)
	}

	send(t, conn, ctx, map[string]any{
		"cmd": "library.install", "ws": workspace.DefaultWorkspace, "kind": "skill", "id": "deploy",
	})
	if e := awaitType(t, conn, ctx, "error"); e["text"] == "" {
		t.Fatal("install was refused with no reason")
	}
}

// The catalog carries a skill's whole body, so the app can show what will go into every prompt
// before anything is written.
func TestLibrary_CatalogCarriesTheBody(t *testing.T) {
	conn, ctx, _, _ := gateDaemonAll(t, auth.ClassApp, withCatalog(t))

	send(t, conn, ctx, map[string]any{"cmd": "library.list"})
	got := awaitType(t, conn, ctx, "library.catalog")
	skills, _ := got["skills"].([]any)
	first, _ := skills[0].(map[string]any)
	if body, _ := first["body"].(string); body != catalogSkill {
		t.Fatalf("the catalog entry carried no body: %v", first)
	}
	// An MCP entry is a URL and an auth mode, both shown in full — and never a client secret.
	servers, _ := got["mcp"].([]any)
	srv, _ := servers[0].(map[string]any)
	if srv["url"] != "https://acme.invalid/mcp" {
		t.Fatalf("the server entry = %v", srv)
	}
	if _, leaked := srv["client_secret"]; leaked {
		t.Error("the listing carried a client secret")
	}
}

// Installing takes an ID and looks the content up server-side. The skill lands the same way a
// hand-assembled folder would, and the workspace picks it up.
func TestLibrary_InstallSkillAndServer(t *testing.T) {
	conn, ctx, _, spaces := gateDaemonAll(t, auth.ClassApp, withCatalog(t))
	ws, _ := spaces.Get(workspace.DefaultWorkspace)

	send(t, conn, ctx, map[string]any{
		"cmd": "library.install", "ws": workspace.DefaultWorkspace, "kind": "skill", "id": "deploy",
	})
	list := awaitType(t, conn, ctx, "skill.list")
	if !skillEntries(list)["deploy"] {
		t.Fatalf("the installed skill is not in the list: %v", list)
	}
	body, err := os.ReadFile(filepath.Join(ws.SkillsDir(), "deploy", "SKILL.md"))
	if err != nil || string(body) != catalogSkill {
		t.Fatalf("the skill on disk = %q, %v", body, err)
	}
	waitFor(t, func() bool { return len(ws.Inventory().Skills) == 1 })

	send(t, conn, ctx, map[string]any{
		"cmd": "library.install", "ws": workspace.DefaultWorkspace, "kind": "mcp", "id": "acme",
	})
	mlist := awaitType(t, conn, ctx, "mcp.list")
	if got := mcpStates(mlist)["acme"]; got != string(workspace.MCPConnecting) {
		t.Fatalf("the first list said %q, want connecting", got)
	}
	if _, err := os.Stat(filepath.Join(ws.MCPDir(), "acme", "mcp.json")); err != nil {
		t.Fatalf("the declaration was not written: %v", err)
	}
}

// Installing the same catalog entry twice is a refusal, not a silent no-op: Discover drops a
// duplicate skill name (first wins), so an overwrite would look like it worked and change nothing.
func TestLibrary_InstallTwiceIsRefused(t *testing.T) {
	conn, ctx, _, _ := gateDaemonAll(t, auth.ClassApp, withCatalog(t))

	send(t, conn, ctx, map[string]any{
		"cmd": "library.install", "ws": workspace.DefaultWorkspace, "kind": "skill", "id": "deploy",
	})
	awaitType(t, conn, ctx, "skill.list")

	send(t, conn, ctx, map[string]any{
		"cmd": "library.install", "ws": workspace.DefaultWorkspace, "kind": "skill", "id": "deploy",
	})
	if e := awaitType(t, conn, ctx, "error"); e["text"] == "" {
		t.Fatal("a second install of the same entry reported success")
	}
}

// An id the catalog does not offer is refused. It is the only thing an install command carries, so
// this is the whole of its input validation.
func TestLibrary_UnknownIDIsRefused(t *testing.T) {
	conn, ctx, _, _ := gateDaemonAll(t, auth.ClassApp, withCatalog(t))

	for _, cmd := range []map[string]any{
		{"cmd": "library.install", "ws": workspace.DefaultWorkspace, "kind": "skill", "id": "nope"},
		{"cmd": "library.install", "ws": workspace.DefaultWorkspace, "kind": "mcp", "id": "nope"},
		{"cmd": "library.install", "ws": workspace.DefaultWorkspace, "kind": "plugin", "id": "deploy"},
	} {
		send(t, conn, ctx, cmd)
		if e := awaitType(t, conn, ctx, "error"); e["text"] == "" {
			t.Fatalf("%v was refused with no reason", cmd)
		}
	}
}

// Without a catalog the library is ABSENT rather than empty, and the daemon reaches out to nothing.
func TestLibrary_UnconfiguredSaysSo(t *testing.T) {
	conn, ctx := gateDaemon(t, auth.ClassApp)

	send(t, conn, ctx, map[string]any{"cmd": "library.list"})
	if e := awaitType(t, conn, ctx, "error"); e["text"] == "" {
		t.Fatal("an unconfigured library answered with no reason")
	}
}
