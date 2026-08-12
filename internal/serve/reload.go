package serve

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// WorkspaceReload re-runs a workspace's discovery (client → server).
//
// It is the answer for everything that changed on disk without going through a command: a skill
// folder copied in, an agent edited, a PERSONA.md rewritten, an MCP server that was down when the
// daemon last looked. There is no watcher on those directories — a ticker would mean re-running every
// MCP handshake on a timer against other people's servers, and a filesystem watcher would mean a
// dependency, recursive watch management and events that get dropped under load (see
// internal/knowledge/watch.go, which made the same call for the same reasons). So it is asked for.
type WorkspaceReload struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// reload re-runs discovery and tells every device what the workspace is made of now.
//
// Detached, like every other reload: it runs each MCP server's handshake, bounded at thirty seconds
// apiece, on a connection whose read loop would otherwise be unable to carry a chat message. Both
// lists go out when it lands, because discovery decides both — this is exactly the command that
// exists for the case where the caller does not know which of them changed.
func (c *conn) reload(ws *workspace.Workspace) {
	log := c.log.With("ws", ws.Name())
	log.Info("reloading the workspace")
	go func() {
		if err := ws.Reload(); err != nil {
			log.Error("reloading the workspace", "err", err)
			return
		}
		c.hub.broadcast(skillList(ws))
		c.hub.broadcast(mcpList(ws))
	}()
}

// handleReload re-runs a workspace's discovery for `nocturn reload`.
//
// The command line has no WebSocket, so this is HTTP — the same shape as POST /pair/code, and for the
// same reason: a CLI that had to open a socket, authenticate and wait for a broadcast to learn what
// happened would be a worse tool than one that asks and is answered.
//
// Answered synchronously, unlike the wire command. There is a person at a terminal who just typed
// this and can wait the seconds the handshakes take, and telling them what the workspace holds now is
// the whole point of them having typed it.
func handleReload(w http.ResponseWriter, r *http.Request, spaces *workspace.Registry, devices *auth.Store, log *slog.Logger) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	caller, ok := devices.Lookup(bearerOf(r))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !capabilitiesOf(caller.Class).manage {
		log.Warn("reload refused", "remote", r.RemoteAddr, "caller", caller.Class)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var body struct {
		Ws string `json:"ws"`
	}
	// An empty body is the default workspace, so `nocturn reload` with no flag needs to send no JSON
	// at all. That is io.EOF specifically, not "any decode error" — a malformed body should be
	// refused rather than quietly reloading a workspace nobody named, and Content-Length cannot be
	// the test because a chunked request does not have one.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Ws == "" {
		body.Ws = workspace.DefaultWorkspace
	}
	ws, ok := spaces.Get(body.Ws)
	if !ok {
		http.Error(w, "unknown workspace", http.StatusNotFound)
		return
	}

	if err := ws.Reload(); err != nil {
		log.Error("reloading the workspace", "ws", body.Ws, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Info("workspace reloaded", "ws", body.Ws, "by", caller.Name)

	inv := ws.Inventory()
	writeJSON(w, map[string]any{
		"ws":      ws.Name(),
		"agents":  len(ws.Agents()),
		"skills":  inv.Skills,
		"plugins": inv.Plugins,
		"mcp":     mcpList(ws).Items,
		"tools":   len(inv.Tools),
	})
}
