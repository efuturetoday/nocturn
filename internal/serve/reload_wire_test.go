package serve

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// A folder copied in by hand is the case this exists for: there is no watcher on skills/, and asking
// is how a running daemon is told to look again. Both lists follow, because discovery decides both.
func TestWorkspaceReload_PicksUpAFolderCopiedIn(t *testing.T) {
	conn, ctx, spaces := gateDaemonSpaces(t, auth.ClassApp)
	ws, _ := spaces.Get(workspace.DefaultWorkspace)

	writeSkillOn(t, spaces, "deploy", "deploy")
	if got := ws.Inventory().Skills; len(got) != 0 {
		t.Fatalf("the daemon noticed a folder nobody told it about: %v", got)
	}

	send(t, conn, ctx, map[string]any{"cmd": "workspace.reload", "ws": workspace.DefaultWorkspace})
	if got := skillEntries(awaitType(t, conn, ctx, "skill.list")); !got["deploy"] {
		t.Fatalf("skill.list after a reload = %v", got)
	}
	awaitType(t, conn, ctx, "mcp.list") // the other half of what discovery decided
	waitFor(t, func() bool { return len(ws.Inventory().Skills) == 1 })
}

// Reloading changes what a workspace can do, so it takes manage.
func TestWorkspaceReload_NeedsManage(t *testing.T) {
	conn, ctx := gateDaemon(t, auth.ClassAppliance)

	send(t, conn, ctx, map[string]any{"cmd": "workspace.reload", "ws": workspace.DefaultWorkspace})
	if e := awaitType(t, conn, ctx, "error"); e["text"] == "" {
		t.Fatal("reload was refused with no reason")
	}
}

// The command line has no socket, so it asks over HTTP — and unlike the wire command it is answered
// synchronously, because somebody typed it and wants to know what the workspace holds now.
func TestHandleReload_AnswersWithTheWorkspace(t *testing.T) {
	addr, devices, spaces := reloadDaemon(t)
	writeSkillOn(t, spaces, "deploy", "deploy")

	bearer, err := devices.Mint("cli", auth.ClassTool)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	res := postReload(t, addr, bearer, `{"ws":"main"}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", res.Status)
	}
	var out struct {
		Ws     string   `json:"ws"`
		Skills []string `json:"skills"`
		Tools  int      `json:"tools"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Ws != workspace.DefaultWorkspace || len(out.Skills) != 1 || out.Tools == 0 {
		t.Fatalf("answer = %+v", out)
	}
}

// The authority is the same as `nocturn pair`'s: a capability, never a class, and never "the request
// came from loopback" — which would be strictly weaker than the 0600 file it would be imitating.
func TestHandleReload_RefusesWithoutManage(t *testing.T) {
	addr, devices, _ := reloadDaemon(t)

	bearer, err := devices.Mint("hallway", auth.ClassAppliance)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	res := postReload(t, addr, bearer, `{}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("an appliance got %s, want 403", res.Status)
	}

	res2 := postReload(t, addr, "not-a-bearer", `{}`)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unknown bearer got %s, want 401", res2.Status)
	}
}

// An empty body means the default workspace, so `nocturn reload` with no flag needs no JSON.
func TestHandleReload_EmptyBodyIsTheDefaultWorkspace(t *testing.T) {
	addr, devices, _ := reloadDaemon(t)
	bearer, _ := devices.Mint("cli", auth.ClassTool)

	res := postReload(t, addr, bearer, "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", res.Status)
	}
	var out struct {
		Ws string `json:"ws"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out.Ws != workspace.DefaultWorkspace {
		t.Fatalf("an empty body reloaded %q", out.Ws)
	}
}

// reloadDaemon brings up a daemon with no socket attached — these tests are about the HTTP route.
func reloadDaemon(t *testing.T) (string, *auth.Store, *workspace.Registry) {
	t.Helper()
	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)

	devices, err := auth.New(t.TempDir() + "/devices.json")
	if err != nil {
		t.Fatalf("device store: %v", err)
	}
	spaces, err := workspace.NewRegistry(workspace.Host{Log: log}, t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	t.Cleanup(spaces.Close)
	return serveTest(t, ctx, spaces, devices, log, defaultHeartbeat), devices, spaces
}

func postReload(t *testing.T, addr, bearer, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/reload", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return res
}

// A malformed body is refused rather than quietly reloading a workspace nobody named. Content-Length
// cannot be the test for "was there a body": a chunked request does not have one.
func TestHandleReload_RefusesAMalformedBody(t *testing.T) {
	addr, devices, _ := reloadDaemon(t)
	bearer, _ := devices.Mint("cli", auth.ClassTool)

	res := postReload(t, addr, bearer, `{"ws":`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a truncated body got %s, want 400", res.Status)
	}
}
