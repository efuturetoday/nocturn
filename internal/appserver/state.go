package appserver

// Workspaces is the STATE service the server consumes — it NEVER touches the filesystem.
// The implementation owns the loaders/parsers (the persona file, agent/skill discovery,
// grantstore, the vault) and hands back typed state; the server sees only that state.
// So swapping a loader (file → keychain → db) never touches the server or the wire
// protocol — the state is the contract, the loader is a hidden detail.
type Workspaces interface {
	// List is the picker view: every workspace and enough to render it.
	List() []WorkspaceSummary
	// Get is the detail view: one workspace's config (no secret values, only presence).
	Get(name string) (WorkspaceState, bool)
	// Open returns the workspace's live turn loop, so the client can stream its chat.
	Open(name string) (Runner, bool)
	// SetPersona persists a new persona for the workspace (the service owns the write).
	SetPersona(name, text string) error
}

// WorkspaceSummary is the list-view state — enough to render the workspace picker.
type WorkspaceSummary struct {
	Name       string `json:"name"`
	Running    bool   `json:"running"`
	Agents     int    `json:"agents"`
	Skills     int    `json:"skills"`
	PersonaSet bool   `json:"personaSet"`
}

// WorkspaceState is the detail-view state — one workspace's config for the app to show
// and edit. It carries connected-account NAMES (presence), never secret values.
type WorkspaceState struct {
	Name     string       `json:"name"`
	Persona  string       `json:"persona"`
	Agents   []AgentInfo  `json:"agents"`
	Skills   []SkillInfo  `json:"skills"`
	Plugins  []PluginInfo `json:"plugins"`
	Accounts []string     `json:"accounts"` // connected-account names — presence only
}

// AgentInfo is one child agent, for the app's agent list.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SkillInfo is one discovered skill.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// PluginInfo is one installed plugin and the tools it exposes.
type PluginInfo struct {
	Name  string   `json:"name"`
	Tools []string `json:"tools"`
}
