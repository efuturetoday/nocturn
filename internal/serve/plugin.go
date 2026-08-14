package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/internal/workspace"
)

// PluginList requests a workspace's installed plugins (client → server).
type PluginList struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// PluginInfo is one installed plugin, as a client sees it.
//
// Names and the tools they contribute, no more. What a plugin may REACH is decided by its manifest
// and asked about at the gate; a list of what is installed is inventory, not authority, so it says
// what is there and lets the catalog entry — which carries the manifest — say what it asks for.
type PluginInfo struct {
	Name  string `json:"name"`
	Tools int    `json:"tools"`
}

// PluginListResult answers plugin.list (server → client).
type PluginListResult struct {
	Type  string       `json:"type"`
	Ws    string       `json:"ws"`
	Items []PluginInfo `json:"items"`
}

// pluginCmd dispatches a plugin.* action.
//
// Listing only, and deliberately so for now: installing goes through library.install, which carries
// an id rather than code, and removing is not built until it can also revoke the remembered
// permission for the hosts its credential rode to — the rule an MCP server's removal already keeps.
// A half-uninstall that left a grant standing would be worse than none.
func (c *conn) pluginCmd(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "plugin.list":
		var m PluginList
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad plugin.list")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		c.send(ctx, pluginList(ws))
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// pluginList renders a workspace's installed plugins.
func pluginList(ws *workspace.Workspace) PluginListResult {
	inv := ws.Inventory()
	items := make([]PluginInfo, 0, len(inv.Plugins))
	for _, name := range inv.Plugins {
		items = append(items, PluginInfo{Name: name, Tools: toolsOf(inv.Tools, name)})
	}
	return PluginListResult{Type: "plugin.list", Ws: ws.Name(), Items: items}
}

// toolsOf counts the workspace tools a plugin contributed. They are namespaced <plugin>_<tool>, which
// is the only link between the two lists — the inventory is derived per call and holds no back
// reference.
func toolsOf(tools []string, plugin string) int {
	n := 0
	prefix := plugin + "_"
	for _, t := range tools {
		if len(t) > len(prefix) && t[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}
