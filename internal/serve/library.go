package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/library"
	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// LibraryList requests the catalog (client → server).
type LibraryList struct {
	Cmd string `json:"cmd"`
}

// LibraryRefresh re-fetches the catalog, then answers like LibraryList (client → server).
type LibraryRefresh struct {
	Cmd string `json:"cmd"`
}

// LibraryInstall installs one catalog entry into a workspace (client → server).
//
// It carries an ID and nothing else, which is the whole security shape of this domain. A command
// carrying a skill BODY would be a way to put arbitrary text into every system prompt of every turn
// — a different authority entirely from "install the thing at position N of a catalog the daemon
// fetched itself". The content is looked up server-side; there is no wire form that supplies it.
// Sideloading stays what it always was: copying a folder on the host.
type LibraryInstall struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
	ID  string `json:"id"` // the catalog entry; what it carries is the entry's business
}

// LibraryEntry is one installable extension, as the catalog offers it — and as a person has to be
// able to judge it before saying yes.
//
// What it CARRIES is a list ("skill", "plugin", "mcp"), because one entry may bring several: an
// integration that brings its own instructions is one thing, not three that share a name. What it
// ASKS FOR is pulled apart here rather than left as JSON for a client to parse: the settings a person
// must supply, the hosts a credential would ride to, the tools it would expose, the base tools its
// guest may call, and the scopes a sign-in would request. That list is the review surface; the
// sandbox decides what code CAN do, the declaration what it wants.
//
// The bodies travel with the listing rather than on demand: the app shows them before installing, and
// a second round trip to fetch what the daemon already holds would only make that step skippable —
// which is the step worth not skipping.
type LibraryEntry struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Carries     []string `json:"carries"`

	Settings []LibrarySetting `json:"settings,omitempty"` // what a person must supply
	Hosts    []string         `json:"hosts,omitempty"`    // where a declared credential would ride
	Tools    []string         `json:"tools,omitempty"`    // what it would expose to the model
	Uses     []string         `json:"uses,omitempty"`     // the base tools a guest may call: its cage
	Scopes   []string         `json:"scopes,omitempty"`   // what a sign-in would ask for
	URL      string           `json:"url,omitempty"`      // the server it would dial

	Skill    string `json:"skill,omitempty"`    // the whole SKILL.md
	Manifest string `json:"manifest,omitempty"` // the shared declaration
	Script   string `json:"script,omitempty"`   // the plugin artifact
}

// LibrarySetting is one value an entry needs before it works, in the form a client renders a field
// from. It never carries a value — those are typed in after installing.
type LibrarySetting struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Label    string   `json:"label,omitempty"`
	Example  string   `json:"example,omitempty"`
	Values   []string `json:"values,omitempty"`
	Optional bool     `json:"optional,omitempty"`
}

// LibraryCatalog carries the catalog (server → client).
type LibraryCatalog struct {
	Type    string         `json:"type"`
	Version string         `json:"version"`
	Entries []LibraryEntry `json:"entries"`
}

// libraryCmd dispatches a library.* action.
//
// Browsing is ungated — a catalog is a list of things that exist, which grants nothing. Installing
// takes `manage`, like every other change to what a workspace is made of.
func (c *conn) libraryCmd(ctx context.Context, cmd string, data []byte) {
	if c.library == nil {
		c.badRequest(ctx, "this daemon has no catalog configured")
		return
	}

	switch cmd {
	case "library.list", "library.refresh":
		cat, err := c.library.Catalog(ctx, cmd == "library.refresh")
		if err != nil {
			c.badRequest(ctx, err.Error())
			return
		}
		c.send(ctx, catalogFrame(cat))
		return
	}

	if !c.can.manage {
		c.badRequest(ctx, "this device may not install into the household's workspaces")
		return
	}

	switch cmd {
	case "library.install":
		var m LibraryInstall
		if err := json.Unmarshal(data, &m); err != nil || m.ID == "" {
			c.badRequest(ctx, "bad library.install")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		if err := c.install(ctx, ws, m.ID); err != nil {
			c.badRequest(ctx, err.Error())
			return
		}
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// install writes one catalog entry into a workspace and makes it take effect.
//
// The content comes from the catalog the daemon fetched, never from the command — see LibraryInstall.
// One folder is written with everything the entry carries, so an integration that brings a server and
// the instructions for it lands as ONE extension with one owner and one credential. Everything after
// that is the same path a hand-assembled folder takes, including the refusal: a folder that exists is
// not overwritten.
func (c *conn) install(ctx context.Context, ws *workspace.Workspace, id string) error {
	item, err := c.library.Item(ctx, id)
	if err != nil {
		return err
	}
	if err := extension.Install(ws.ExtensionsDir(), extension.Package{
		Name:           item.ID,
		Manifest:       item.Manifest,
		Skill:          item.Skill,
		PluginManifest: item.PluginManifest,
		PluginScript:   item.PluginScript,
		MCP:            item.MCP,
	}); err != nil {
		return err
	}
	c.log.Info("installed from the catalog", "ws", ws.Name(), "id", id, "carries", carriedBy(item))
	c.applyInstall(ws, item)
	return nil
}

// carriedBy names what an entry brought, for the log line an operator reads.
func carriedBy(it library.Item) []string {
	var out []string
	for _, p := range []extension.Payload{extension.PayloadSkill, extension.PayloadPlugin, extension.PayloadMCP} {
		if it.Carries(p) {
			out = append(out, string(p))
		}
	}
	return out
}

// applyInstall reloads the workspace and tells every device what is installed now — one list per
// payload the entry brought, because that is how the app groups them.
func (c *conn) applyInstall(ws *workspace.Workspace, it library.Item) {
	log := c.log.With("ws", ws.Name(), "id", it.ID)
	// A server's handshake takes seconds, so its list goes out FIRST with the new entry marked as
	// connecting — otherwise a device sits on a stale list until the reload finishes and cannot tell
	// "not installed" from "not connected yet".
	if it.Carries(extension.PayloadMCP) {
		first := mcpList(ws)
		first.Items = append(first.Items, MCPInfo{Name: it.ID, State: string(workspace.MCPConnecting)})
		c.send(context.Background(), first)
	}
	go func() {
		if err := ws.Reload(); err != nil {
			log.Error("reloading the workspace after an install", "err", err)
			return
		}
		if it.Carries(extension.PayloadSkill) {
			c.hub.broadcast(skillList(ws))
		}
		if it.Carries(extension.PayloadPlugin) {
			c.hub.broadcast(pluginList(ws))
		}
		if it.Carries(extension.PayloadMCP) {
			c.hub.broadcast(mcpList(ws))
		}
	}()
}

// applyPlugins makes an installed plugin take effect and tells every device what is installed now.
//
// The reload is detached for the reason applySkills gives — it re-runs the whole of discovery, MCP
// handshakes included, on a goroutine so this connection keeps reading. The list goes out AFTER it
// here, unlike a skill's: a plugin's tools exist only once discovery has built them, so a list sent
// first would name a plugin whose tools are not there yet.
func (c *conn) applyPlugins(ws *workspace.Workspace, name string) {
	log := c.log.With("ws", ws.Name(), "plugin", name)
	go func() {
		if err := ws.Reload(); err != nil {
			log.Error("reloading the workspace after a plugin was installed", "err", err)
			return
		}
		c.hub.broadcast(pluginList(ws))
	}()
}

// catalogFrame renders the catalog for the wire, summarising each entry's declaration so a client can
// show the grant without parsing JSON.
//
// A declaration that will not parse is summarised as nothing rather than dropped: library.parse
// already refused those, so reaching here with one would be a bug, and a listing that silently
// omitted an entry the daemon does offer would be worse than one showing an empty cage.
func catalogFrame(cat *library.Catalog) LibraryCatalog {
	out := LibraryCatalog{
		Type:    "library.catalog",
		Version: cat.Version,
		Entries: make([]LibraryEntry, 0, len(cat.Items)),
	}
	for _, it := range cat.Items {
		out.Entries = append(out.Entries, entryFrame(it))
	}
	return out
}

// entryFrame renders one catalog entry with what it carries and what it asks for.
func entryFrame(it library.Item) LibraryEntry {
	e := LibraryEntry{
		ID:          it.ID,
		Title:       it.Title,
		Description: it.Description,
		Homepage:    it.Homepage,
		Tags:        it.Tags,
		Carries:     carriedBy(it),
		Skill:       it.Skill,
		Manifest:    it.Manifest,
		Script:      it.PluginScript,
	}
	if it.Manifest != "" {
		// The same reader the install uses, so what the app renders a form from is a declaration that
		// passed Validate — not one a second unmarshal here happened to accept.
		if d, err := extension.ParseDecl([]byte(it.Manifest)); err == nil {
			for _, c := range d.Config {
				e.Settings = append(e.Settings, LibrarySetting{
					Name: c.Name, Type: string(c.Type), Label: c.Label,
					Example: c.Example, Values: c.Values, Optional: c.Optional,
				})
			}
			for _, c := range d.Credentials {
				if c.Host != "" {
					e.Hosts = append(e.Hosts, c.Host)
				}
			}
		}
	}
	if it.Carries(extension.PayloadPlugin) {
		var m plugin.Manifest
		if json.Unmarshal([]byte(it.PluginManifest), &m) == nil {
			for _, t := range m.Tools {
				// As the model will see them. A bare "search" says nothing about which search, and
				// the namespaced name is also what an error message will name.
				e.Tools = append(e.Tools, m.Name+"_"+t.Name)
			}
			e.Uses = append(e.Uses, m.Uses...)
			for _, c := range m.Credentials {
				e.Hosts = append(e.Hosts, c.Host)
			}
			// The client id and secret stay here: what a person needs before agreeing is which access
			// is about to be requested in their name.
			for _, o := range m.OAuth {
				e.Scopes = append(e.Scopes, o.Scopes...)
			}
		}
	}
	if it.Carries(extension.PayloadMCP) {
		if srv, err := mcp.Parse([]byte(it.MCP), it.ID); err == nil {
			e.URL = srv.URL
			if srv.OAuth != nil {
				e.Scopes = append(e.Scopes, srv.OAuth.Scopes...)
			}
		}
	}
	return e
}
