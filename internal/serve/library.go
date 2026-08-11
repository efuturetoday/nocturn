package serve

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/efuturetoday/nocturn/internal/library"
	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/skill"
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
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Kind string `json:"kind"` // "skill" | "mcp"
	ID   string `json:"id"`
}

// LibrarySkill is one installable skill, as the catalog offers it.
type LibrarySkill struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Body        string   `json:"body"`
}

// LibraryServer is one installable MCP server, as the catalog offers it.
type LibraryServer struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	Auth        string   `json:"auth,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
}

// LibraryCatalog carries the catalog (server → client).
type LibraryCatalog struct {
	Type    string          `json:"type"`
	Version string          `json:"version"`
	Skills  []LibrarySkill  `json:"skills"`
	MCP     []LibraryServer `json:"mcp"`
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
		if err := c.install(ctx, ws, m.Kind, m.ID); err != nil {
			c.badRequest(ctx, err.Error())
			return
		}
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// install writes one catalog entry into a workspace and makes it take effect.
//
// The content comes from the catalog the daemon fetched, never from the command — see
// LibraryInstall. Everything after that is the same path a hand-assembled folder takes, including
// the refusals: a skill whose name is already held, a server whose folder exists.
func (c *conn) install(ctx context.Context, ws *workspace.Workspace, kind, id string) error {
	switch kind {
	case "skill":
		item, err := c.library.Skill(ctx, id)
		if err != nil {
			return err
		}
		if _, err := skill.Write(ws.SkillsDir(), item.Folder, item.Body); err != nil {
			return err
		}
		c.log.Info("installed a skill from the catalog", "ws", ws.Name(), "id", id, "folder", item.Folder)
		c.applySkills(ws, "install", item.Folder)
		return nil

	case "mcp":
		item, err := c.library.Server(ctx, id)
		if err != nil {
			return err
		}
		srv := mcp.Server{Name: item.Name, URL: item.URL, Auth: item.Auth, OAuth: item.OAuth}
		if err := mcp.Write(ws.MCPDir(), srv); err != nil {
			return err
		}
		c.log.Info("installed an MCP server from the catalog", "ws", ws.Name(), "id", id, "server", item.Name)
		first := mcpList(ws)
		first.Items = append(first.Items, MCPInfo{
			Name: srv.Name, URL: srv.URL, State: string(workspace.MCPConnecting),
		})
		c.applyMCP(ws, "install", item.Name, first)
		return nil

	default:
		return fmt.Errorf("unknown kind %q (want \"skill\" or \"mcp\")", kind)
	}
}

// catalogFrame renders the catalog for the wire.
//
// A skill's whole body goes out with the listing rather than on demand. The app shows it before
// installing, and a second round-trip to fetch what the daemon already holds would only make that
// step skippable — which is the step that is worth not skipping. An MCP entry has no body: it is a
// URL and an auth mode, and both are shown in full.
func catalogFrame(cat *library.Catalog) LibraryCatalog {
	out := LibraryCatalog{
		Type:    "library.catalog",
		Version: cat.Version,
		Skills:  make([]LibrarySkill, 0, len(cat.Skills)),
		MCP:     make([]LibraryServer, 0, len(cat.MCP)),
	}
	for _, it := range cat.Skills {
		out.Skills = append(out.Skills, LibrarySkill{
			ID:          it.ID,
			Title:       it.Title,
			Description: it.Description,
			Homepage:    it.Homepage,
			Tags:        it.Tags,
			Body:        it.Body,
		})
	}
	for _, it := range cat.MCP {
		e := LibraryServer{
			ID:          it.ID,
			Title:       it.Title,
			Description: it.Description,
			Homepage:    it.Homepage,
			Tags:        it.Tags,
			Name:        it.Name,
			URL:         it.URL,
			Auth:        it.Auth,
		}
		// The scopes a sign-in would ask for, so consent is informed before the browser opens. The
		// client id and secret stay here: they are ours, not the user's, and a listing is not the
		// place for them.
		if it.OAuth != nil {
			e.Scopes = it.OAuth.Scopes
		}
		out.MCP = append(out.MCP, e)
	}
	return out
}
