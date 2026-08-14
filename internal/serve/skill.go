package serve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// SkillList requests a workspace's skills (client → server).
type SkillList struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// SkillRead requests one skill's SKILL.md verbatim (client → server).
type SkillRead struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Name string `json:"name"`
}

// SkillEnable switches a skill on or off (client → server).
type SkillEnable struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Name string `json:"name"`
	On   bool   `json:"on"`
}

// SkillRemove deletes a skill's directory (client → server).
type SkillRemove struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Name string `json:"name"`
}

// SkillInfo is one skill in the list.
type SkillInfo struct {
	Name        string `json:"name"`
	Folder      string `json:"folder"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	Bytes       int    `json:"bytes"`
	// Plugin names the plugin that BUNDLED this skill, empty for a skill of its own in skills/. Such
	// a skill can be neither switched off nor deleted here: it belongs to the plugin and goes when it
	// does. It is listed anyway, because it is in front of the model — a page that said "no skills"
	// while the prompt carried one would be lying about the only thing it exists to show.
	Plugin string `json:"plugin,omitempty"`
}

// SkillListResult carries a workspace's skills (server → client).
type SkillListResult struct {
	Type  string      `json:"type"`
	Ws    string      `json:"ws"`
	Items []SkillInfo `json:"items"`
}

// SkillBody carries one skill's SKILL.md (server → client).
type SkillBody struct {
	Type string `json:"type"`
	Ws   string `json:"ws"`
	Name string `json:"name"`
	Body string `json:"body"`
}

// skillCmd dispatches a skill.* action.
//
// Listing and reading are ungated, which is the same call ADR-10 makes about skill_read: a skill is
// CONTEXT, never authority — it shapes how the model uses its gated tools and grants nothing on its
// own. So any paired device may read one, including an appliance. Changing the set takes `manage`.
func (c *conn) skillCmd(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "skill.list":
		var m SkillList
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad skill.list")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		c.sendSkills(ctx, ws)
		return

	case "skill.read":
		var m SkillRead
		if err := json.Unmarshal(data, &m); err != nil || m.Name == "" {
			c.badRequest(ctx, "bad skill.read")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		body, err := skill.Read(ws.SkillsDir(), m.Name)
		if err != nil {
			// A skill a plugin bundled has no folder under skills/, and reading it is the whole point
			// of listing it: what the model is told is exactly what a person opens this to see.
			bundled, ok := bundledBody(ws, m.Name)
			if !ok {
				c.badRequest(ctx, err.Error())
				return
			}
			body = bundled
		}
		c.send(ctx, SkillBody{Type: "skill.body", Ws: ws.Name(), Name: m.Name, Body: body})
		return
	}

	if !c.can.manage {
		c.badRequest(ctx, "this device may not manage the household's skills")
		return
	}

	switch cmd {
	case "skill.enable":
		var m SkillEnable
		if err := json.Unmarshal(data, &m); err != nil || m.Name == "" {
			c.badRequest(ctx, "bad skill.enable")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		if err := skill.SetEnabled(ws.SkillsDir(), m.Name, m.On); err != nil {
			c.badRequest(ctx, err.Error())
			return
		}
		c.applySkills(ws, "enable", m.Name)

	case "skill.remove":
		var m SkillRemove
		if err := json.Unmarshal(data, &m); err != nil || m.Name == "" {
			c.badRequest(ctx, "bad skill.remove")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		if err := skill.Remove(ws.SkillsDir(), m.Name); err != nil {
			c.badRequest(ctx, err.Error())
			return
		}
		c.applySkills(ws, "remove", m.Name)

	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// applySkills makes a change to skills/ take effect and tells every device what the set is now.
//
// The list goes out FIRST, from what is already on disk, and the reload runs after. The disk is what
// the next start will see, so it is the honest answer either way — and a reload re-runs the whole of
// discovery, including MCP handshakes that are allowed to take seconds. Waiting for that before
// answering would freeze this connection, and it runs on the read loop: the device could not even
// send a chat message meanwhile.
//
// It is detached rather than awaited for the same reason. Reload is single-flight, so two devices
// toggling at once queue rather than interleave, and a failure leaves the previous discovery
// standing — the set on screen is still what the daemon will have.
func (c *conn) applySkills(ws *workspace.Workspace, action, name string) {
	c.hub.broadcast(skillList(ws))
	log := c.log.With("ws", ws.Name(), "skill", name, "action", action)
	log.Info("skills changed")
	go func() {
		if err := ws.Reload(); err != nil {
			log.Error("reloading the workspace after a skill changed", "err", err)
		}
	}()
}

// sendSkills answers this connection with a workspace's skills.
func (c *conn) sendSkills(ctx context.Context, ws *workspace.Workspace) {
	c.send(ctx, skillList(ws))
}

// skillList renders a workspace's skills for the wire, disabled ones included — a list that omitted
// them could not offer switching one back on.
func skillList(ws *workspace.Workspace) SkillListResult {
	entries, err := skill.List(ws.SkillsDir())
	if err != nil {
		entries = nil
	}
	items := make([]SkillInfo, 0, len(entries))
	for _, e := range entries {
		items = append(items, SkillInfo{
			Name:        e.Name,
			Folder:      e.Folder,
			Description: e.Description,
			Enabled:     e.Enabled,
			Bytes:       e.Bytes,
		})
	}
	items = append(items, bundledSkills(ws, items)...)
	return SkillListResult{Type: "skill.list", Ws: ws.Name(), Items: items}
}

// bundledBody returns the SKILL.md of a plugin-bundled skill by NAME (which need not be its folder).
//
// It answers from the same list the wire shows, so the precedence holds here too: a bundled skill
// whose name a hand-written one already holds is NOT in that list, and reading it would hand back a
// body the model never sees.
func bundledBody(ws *workspace.Workspace, name string) (string, bool) {
	own, err := skill.List(ws.SkillsDir())
	if err != nil {
		own = nil
	}
	have := make([]SkillInfo, 0, len(own))
	for _, e := range own {
		have = append(have, SkillInfo{Name: e.Name})
	}
	for _, s := range bundledSkills(ws, have) {
		if s.Name != name {
			continue
		}
		body, err := os.ReadFile(filepath.Join(ws.PluginsDir(), s.Plugin, plugin.SkillFile))
		if err != nil {
			return "", false
		}
		return string(body), true
	}
	return "", false
}

// bundledSkills lists the skills installed plugins brought with them, skipping any whose name a
// hand-written skill already holds — the same precedence the workspace applies when it folds them
// into the set, so this list says what the model actually got.
func bundledSkills(ws *workspace.Workspace, have []SkillInfo) []SkillInfo {
	taken := make(map[string]bool, len(have))
	for _, s := range have {
		taken[s.Name] = true
	}
	var out []SkillInfo
	for folder, body := range plugin.SkillBodies(ws.PluginsDir()) {
		sk, err := skill.Parse(body, folder)
		if err != nil || taken[sk.Name] {
			continue
		}
		out = append(out, SkillInfo{
			Name:        sk.Name,
			Folder:      "plugins/" + folder,
			Description: sk.Description,
			Enabled:     true,
			Bytes:       len(body),
			Plugin:      folder,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
