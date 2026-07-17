package main

import (
	"fmt"

	"github.com/efuturetoday/nocturn/internal/appserver"
	"github.com/efuturetoday/nocturn/internal/persona"
)

// appWorkspaces adapts the built workspace registry (the bound-map) to the appserver
// Workspaces STATE service. It reads state from each workspace's own services (persona,
// agents, skills) and its runner — never the filesystem. So the app server stays FS-free:
// swapping how a workspace loads its state never touches this adapter's shape.
type appWorkspaces struct {
	bounds map[string]*bound
	names  []string // sorted, for a stable List order
}

var _ appserver.Workspaces = (*appWorkspaces)(nil)

// List is the picker view — every workspace with its live running flag and counts.
func (a *appWorkspaces) List() []appserver.WorkspaceSummary {
	out := make([]appserver.WorkspaceSummary, 0, len(a.names))
	for _, name := range a.names {
		b := a.bounds[name]
		out = append(out, appserver.WorkspaceSummary{
			Name:       name,
			Running:    b.runner.Snapshot().Running,
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

// Open returns the workspace's live turn loop so the client can stream its chat.
func (a *appWorkspaces) Open(name string) (appserver.Runner, bool) {
	b, ok := a.bounds[name]
	if !ok {
		return nil, false
	}
	return b.runner, true
}

// SetPersona persists a new persona via the workspace's persona service.
func (a *appWorkspaces) SetPersona(name, text string) error {
	b, ok := a.bounds[name]
	if !ok {
		return fmt.Errorf("unknown workspace %q", name)
	}
	return b.ws.SetPersona(text)
}
