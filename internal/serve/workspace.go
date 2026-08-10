package serve

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/efuturetoday/nocturn/internal/workspace"
)

// WorkspaceList requests the daemon's workspaces (client → server).
type WorkspaceList struct {
	Cmd string `json:"cmd"`
}

// WorkspaceCreate adds a workspace (client → server). Name is its identity — a directory, the input
// to its vault key, the ws field on every later command — and Title is only what to show.
type WorkspaceCreate struct {
	Cmd   string `json:"cmd"`
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
}

// WorkspaceRename changes a workspace's DISPLAY name. The identity is untouched: renaming the folder
// would re-key its vault and every plugin/MCP shard into unreadability — see workspace.Workspace.SetTitle.
type WorkspaceRename struct {
	Cmd   string `json:"cmd"`
	Name  string `json:"name"`
	Title string `json:"title"`
}

// WorkspaceDelete closes a workspace and moves its directory to the trash (client → server).
type WorkspaceDelete struct {
	Cmd  string `json:"cmd"`
	Name string `json:"name"`
}

// WorkspaceInfo is one workspace in the list.
type WorkspaceInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Default bool   `json:"default,omitempty"`
}

// WorkspaceListResult carries the daemon's workspaces (server → client).
type WorkspaceListResult struct {
	Type  string          `json:"type"`
	Items []WorkspaceInfo `json:"items"`
}

// workspaceCmd dispatches a workspace.* action.
//
// Listing is ungated — which workspaces exist is context, and every paired device already addresses
// one on every chat command. Changing the set is not: it takes `manage`, which an appliance does not
// have (see capability.go).
func (c *conn) workspaceCmd(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "workspace.list":
		c.sendWorkspaces(ctx)
		return
	}

	if !c.can.manage {
		c.badRequest(ctx, "this device may not manage the household's workspaces")
		return
	}

	switch cmd {
	case "workspace.create":
		var m WorkspaceCreate
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad workspace.create")
			return
		}
		if _, err := c.spaces.Create(m.Name, m.Title); err != nil {
			c.badRequest(ctx, err.Error())
			return
		}
		c.log.Info("workspace created", "ws", m.Name)
	case "workspace.rename":
		var m WorkspaceRename
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad workspace.rename")
			return
		}
		if err := c.spaces.SetTitle(m.Name, m.Title); err != nil {
			c.badRequest(ctx, err.Error())
			return
		}
	case "workspace.delete":
		var m WorkspaceDelete
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad workspace.delete")
			return
		}
		if err := c.spaces.Delete(m.Name); err != nil {
			c.badRequest(ctx, err.Error())
			return
		}
		c.log.Info("workspace deleted", "ws", m.Name)
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
		return
	}

	// Broadcast rather than answer: the set is daemon-wide, so every attached device converges on it
	// instead of the one that asked learning about a change the others do not see. Same shape as
	// device.forget.
	c.hub.broadcast(workspaceList(c.spaces))
}

// sendWorkspaces answers this connection with the current set.
func (c *conn) sendWorkspaces(ctx context.Context) {
	c.send(ctx, workspaceList(c.spaces))
}

// workspaceList renders the registry for the wire, sorted by name — a set that reshuffles itself
// between two looks is unreadable.
func workspaceList(spaces *workspace.Registry) WorkspaceListResult {
	open := spaces.Snapshot()
	items := make([]WorkspaceInfo, 0, len(open))
	for _, ws := range open {
		items = append(items, WorkspaceInfo{
			Name:    ws.Name(),
			Title:   ws.Title(),
			Default: ws.Name() == workspace.DefaultWorkspace,
		})
	}
	slices.SortFunc(items, func(a, b WorkspaceInfo) int { return strings.Compare(a.Name, b.Name) })
	return WorkspaceListResult{Type: "workspace.list", Items: items}
}
