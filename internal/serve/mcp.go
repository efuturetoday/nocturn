package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// MCPList requests a workspace's MCP servers and how each one fared (client → server).
type MCPList struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// MCPAdd declares a server (client → server). No credential rides along: a token is seeded with
// `nocturn secret set`, and OAuth runs through the auth.* domain this app already speaks.
type MCPAdd struct {
	Cmd   string         `json:"cmd"`
	Ws    string         `json:"ws"`
	Name  string         `json:"name"`
	URL   string         `json:"url"`
	Auth  string         `json:"auth,omitempty"`
	OAuth *mcp.OAuthDecl `json:"oauth,omitempty"`
}

// MCPRemove drops a server's folder — its declaration and its secret shard (client → server).
type MCPRemove struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Name string `json:"name"`
}

// MCPReconnect re-runs the workspace's discovery, so a server that was down or newly authorized is
// tried again (client → server).
type MCPReconnect struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// MCPInfo is one declared server and what became of it.
type MCPInfo struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	State string `json:"state"`
	Tools int    `json:"tools"`
	Note  string `json:"note,omitempty"`
}

// MCPListResult carries a workspace's servers (server → client).
type MCPListResult struct {
	Type  string    `json:"type"`
	Ws    string    `json:"ws"`
	Items []MCPInfo `json:"items"`
}

// mcpCmd dispatches an mcp.* action.
//
// Listing is ungated: which servers a workspace declares, and which of them failed, is the same kind
// of fact as which workspaces exist. Declaring or dropping one takes `manage`.
func (c *conn) mcpCmd(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "mcp.list":
		var m MCPList
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad mcp.list")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		c.send(ctx, mcpList(ws))
		return
	}

	if !c.can.manage {
		c.badRequest(ctx, "this device may not manage the household's MCP servers")
		return
	}

	switch cmd {
	case "mcp.add":
		var m MCPAdd
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad mcp.add")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		srv := mcp.Server{Name: m.Name, URL: m.URL, Auth: m.Auth, OAuth: m.OAuth}
		if err := mcp.Write(ws.MCPDir(), srv); err != nil {
			c.badRequest(ctx, err.Error())
			return
		}
		// The declaration is on disk and the handshake has not run, so the server is CONNECTING and
		// the list says so. Reporting "failed" for a server nobody has tried yet would be a lie, and
		// reporting nothing would leave a device staring at a list its own command is missing from.
		c.applyMCP(ws, "add", m.Name, connectingFor(srv))

	case "mcp.remove":
		var m MCPRemove
		if err := json.Unmarshal(data, &m); err != nil || m.Name == "" {
			c.badRequest(ctx, "bad mcp.remove")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		// The host BEFORE the folder goes, because that is the only place its URL is written down.
		var host string
		for _, s := range ws.Inventory().MCP {
			if s.Name == m.Name {
				host, _ = mcp.Host(s.URL)
			}
		}
		if err := mcp.Remove(ws.MCPDir(), m.Name); err != nil {
			c.badRequest(ctx, err.Error())
			return
		}
		// A remembered "yes" for this host outlives the server it was given for, and the next server
		// declared on the same host would inherit it. See Workspace.ForgetNetAccess for what that
		// costs the other way.
		if host != "" && ws.ForgetNetAccess(host) {
			c.log.Info("revoked the remembered network grant of a removed MCP server",
				"ws", ws.Name(), "server", m.Name, "host", host)
		}
		c.applyMCP(ws, "remove", m.Name, nil)

	case "mcp.reconnect":
		var m MCPReconnect
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad mcp.reconnect")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		c.applyMCP(ws, "reconnect", "", nil)

	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// applyMCP answers with the set as it stands, then reloads the workspace and answers again.
//
// Twice on purpose. A reload runs every server's handshake, each bounded at thirty seconds, and it
// runs on this connection's read loop — awaiting it would leave the device unable to send so much as
// a chat message meanwhile. So the first list goes out immediately, carrying `connecting` for a
// server that has not been tried, and the second carries what actually happened.
func (c *conn) applyMCP(ws *workspace.Workspace, action, name string, pending []MCPInfo) {
	first := mcpList(ws)
	first.Items = append(first.Items, pending...)
	c.hub.broadcast(first)

	log := c.log.With("ws", ws.Name(), "server", name, "action", action)
	log.Info("mcp servers changed")
	go func() {
		if err := ws.Reload(); err != nil {
			log.Error("reloading the workspace after an MCP change", "err", err)
			return
		}
		c.hub.broadcast(mcpList(ws))
	}()
}

// connectingFor is the placeholder entry for a server that is declared but not yet tried.
func connectingFor(s mcp.Server) []MCPInfo {
	return []MCPInfo{{Name: s.Name, URL: s.URL, State: string(workspace.MCPConnecting)}}
}

// mcpList renders a workspace's servers for the wire. It reports one entry per DECLARED server,
// connected or not — a server you configured that did not come up is exactly what this list is
// opened to find, and one that silently omitted it could not say so.
func mcpList(ws *workspace.Workspace) MCPListResult {
	status := ws.Inventory().MCP
	items := make([]MCPInfo, 0, len(status))
	for _, s := range status {
		items = append(items, MCPInfo{
			Name:  s.Name,
			URL:   s.URL,
			State: string(s.State),
			Tools: s.Tools,
			Note:  s.Note,
		})
	}
	return MCPListResult{Type: "mcp.list", Ws: ws.Name(), Items: items}
}
