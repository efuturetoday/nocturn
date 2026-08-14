package serve

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/efuturetoday/nocturn/internal/plugin"
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

// PluginRemove deletes a plugin from a workspace (client → server).
type PluginRemove struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Name string `json:"name"`
}

// pluginCmd dispatches a plugin.* action.
//
// Listing is ungated inventory; removing takes `manage`, like every other change to what a workspace
// is made of. Installing is not here at all — it goes through library.install, which carries an id
// and never code.
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

	case "plugin.remove":
		if !c.can.manage {
			c.badRequest(ctx, "this device may not change the household's workspaces")
			return
		}
		var m PluginRemove
		if err := json.Unmarshal(data, &m); err != nil || m.Name == "" {
			c.badRequest(ctx, "bad plugin.remove")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		c.removePlugin(ctx, ws, m.Name)

	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// removePlugin deletes a plugin and takes its standing permissions with it.
//
// The hosts come from the MANIFEST and are read BEFORE the folder goes, because the folder is the
// only place they are written down — and from the declaration on disk rather than from the live
// inventory, so a plugin installed moments ago (whose reload may still be running) is not the one
// case that silently skips the revocation.
//
// Why revoke at all: a grant records (Kind, Target) and nothing about WHY it was given. Once the
// program that prompted the question is gone, the answer stands on its own, and the next thing to
// reach that host inherits a permission nobody gave for it. The trade cuts both ways — the same
// grant may be the one given so http_read could reach that host — and being asked once more is the
// cheap side of it. This is the rule mcp.remove already keeps; a plugin is no different.
//
// The token goes too: plugin.Remove deletes the folder, and the secret shard lives in it.
func (c *conn) removePlugin(ctx context.Context, ws *workspace.Workspace, name string) {
	var hosts []string
	if loaded, err := plugin.Load(filepath.Join(ws.PluginsDir(), name)); err == nil {
		for _, cred := range loaded.Manifest.Credentials {
			hosts = append(hosts, cred.Host)
		}
	} else {
		c.log.Warn("could not read the plugin's manifest before removing it",
			"ws", ws.Name(), "plugin", name, "err", err)
	}

	if err := plugin.Remove(ws.PluginsDir(), name); err != nil {
		c.badRequest(ctx, err.Error())
		return
	}
	for _, host := range hosts {
		if ws.ForgetNetAccess(host) {
			c.log.Info("revoked the remembered network grant of a removed plugin",
				"ws", ws.Name(), "plugin", name, "host", host)
		}
	}
	c.log.Info("removed a plugin", "ws", ws.Name(), "plugin", name, "hosts", hosts)
	c.applyPlugins(ws, name)
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
