package serve

import (
	"context"
	"sort"
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
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		c.send(ctx, WorkspaceListResult{Type: "workspace.list", Items: items})
	default:
		c.send(ctx, newError("unknown action: "+cmd))
	}
}
