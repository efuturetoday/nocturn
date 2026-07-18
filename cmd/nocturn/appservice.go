package main

import (
	"fmt"
	"time"

	"github.com/efuturetoday/nocturn/internal/appserver"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/persona"
)

// appWorkspaces adapts the built workspace registry (the bound-map) to the appserver
// Workspaces STATE service. It reads state from each workspace's own services (persona,
// agents, skills) and its chats — never the filesystem. So the app server stays FS-free:
// swapping how a workspace loads its state never touches this adapter's shape.
type appWorkspaces struct {
	bounds map[string]*bound
	names  []string // sorted, for a stable List order
	sync   *syncHub // client-sync fan-out (badges + list changes) — the managers' producer side
}

var _ appserver.Workspaces = (*appWorkspaces)(nil)

// List is the picker view — every workspace with its live running flag and counts.
func (a *appWorkspaces) List() []appserver.WorkspaceSummary {
	out := make([]appserver.WorkspaceSummary, 0, len(a.names))
	for _, name := range a.names {
		b := a.bounds[name]
		out = append(out, appserver.WorkspaceSummary{
			Name:       name,
			Running:    b.chats.AnyRunning(),
			Agents:     len(b.ws.Agents()),
			Skills:     b.ws.Skills().Len(),
			PersonaSet: b.ws.Persona() != persona.Default,
		})
	}
	return out
}

// Get is the detail view — one workspace's config, read from its services.
func (a *appWorkspaces) Get(name string) (appserver.WorkspaceState, bool) {
	b, ok := a.bounds[name]
	if !ok {
		return appserver.WorkspaceState{}, false
	}
	st := appserver.WorkspaceState{Name: name, Persona: b.ws.Persona()}
	for _, ag := range b.ws.Agents() {
		st.Agents = append(st.Agents, appserver.AgentInfo{Name: ag.Name, Description: ag.Description})
	}
	for _, sk := range b.ws.Skills().Skills() {
		st.Skills = append(st.Skills, appserver.SkillInfo{Name: sk.Name, Description: sk.Description})
	}
	// Plugins + connected-account presence are not yet surfaced as workspace state — TODO
	// when the plugin registry and vault expose a listing (they load in cmd today).
	return st, true
}

// SetPersona persists a new persona via the workspace's persona service.
func (a *appWorkspaces) SetPersona(name, text string) error {
	b, ok := a.bounds[name]
	if !ok {
		return fmt.Errorf("unknown workspace %q", name)
	}
	return b.ws.SetPersona(text)
}

// --- chats: each workspace's chat.Manager owns the live chats + persistence ---

// Chats lists a workspace's chats (most recent first), read from its chat manager.
func (a *appWorkspaces) Chats(ws string) ([]appserver.ChatMeta, bool) {
	b, ok := a.bounds[ws]
	if !ok {
		return nil, false
	}
	metas := b.chats.List()
	out := make([]appserver.ChatMeta, 0, len(metas))
	for _, m := range metas {
		out = append(out, toChatMeta(m))
	}
	return out, true
}

// NewChat creates an empty chat in the workspace; its loop spins on first OpenChat.
func (a *appWorkspaces) NewChat(ws, name string) (appserver.ChatMeta, bool) {
	b, ok := a.bounds[ws]
	if !ok {
		return appserver.ChatMeta{}, false
	}
	m, err := b.chats.New(name, chat.OriginUser) // an app-created chat is a human conversation
	if err != nil {
		return appserver.ChatMeta{}, false
	}
	return toChatMeta(m), true
}

// OpenChat returns the chat's live turn loop (lazily spun by the manager).
func (a *appWorkspaces) OpenChat(ws, id string) (appserver.Runner, bool) {
	b, ok := a.bounds[ws]
	if !ok {
		return nil, false
	}
	return b.chats.Open(id)
}

// RenameChat updates a chat's name; false for an unknown workspace or a failed write.
func (a *appWorkspaces) RenameChat(ws, id, name string) bool {
	b, ok := a.bounds[ws]
	if !ok {
		return false
	}
	return b.chats.Rename(id, name) == nil
}

// DeleteChat stops the live chat and removes it; false for an unknown workspace.
func (a *appWorkspaces) DeleteChat(ws, id string) bool {
	b, ok := a.bounds[ws]
	if !ok {
		return false
	}
	return b.chats.Delete(id) == nil
}

// WatchSync subscribes to the one client-sync stream (all workspaces' managers feed the one
// hub — badges + list changes; the app server fans it to the client).
func (a *appWorkspaces) WatchSync() (<-chan appserver.Sync, func()) {
	return a.sync.Watch()
}

// toChatMeta maps a chat.Meta (domain) to the wire ChatMeta (RFC3339 timestamp).
func toChatMeta(m chat.Meta) appserver.ChatMeta {
	return appserver.ChatMeta{
		ID:      m.ID,
		Name:    m.Name,
		Origin:  string(m.Origin),
		Agent:   m.Agent,
		Updated: m.Updated.Format(time.RFC3339),
		Turns:   m.Turns,
	}
}
