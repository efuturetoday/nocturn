package serve

import (
	"context"
	"slices"
	"strings"
)

// WorkspaceList requests the daemon's workspaces (client → server).
type WorkspaceList struct {
	Cmd string `json:"cmd"`
}

// WorkspaceInfo is one workspace in the list.
type WorkspaceInfo struct {
	Name string `json:"name"`
}

// WorkspaceListResult carries the daemon's workspaces (server → client).
type WorkspaceListResult struct {
	Type  string          `json:"type"`
	Items []WorkspaceInfo `json:"items"`
}

// workspaceCmd dispatches a workspace.* action.
func (c *conn) workspaceCmd(ctx context.Context, cmd string) {
	switch cmd {
	case "workspace.list":
		items := make([]WorkspaceInfo, 0, len(c.spaces))
		for name := range c.spaces {
			items = append(items, WorkspaceInfo{Name: name})
		}
		slices.SortFunc(items, func(a, b WorkspaceInfo) int { return strings.Compare(a.Name, b.Name) })
		c.send(ctx, WorkspaceListResult{Type: "workspace.list", Items: items})
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}
