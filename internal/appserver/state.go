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
	// SetPersona persists a new persona for the workspace (the service owns the write).
	SetPersona(name, text string) error

	// --- chats: a workspace holds several named chats, each with saved history ---

	// Chats lists a workspace's chats (most recent first). ok is false for an unknown ws.
	Chats(ws string) ([]ChatMeta, bool)
	// NewChat creates an empty chat in the workspace and returns its metadata.
	NewChat(ws, name string) (ChatMeta, bool)
	// OpenChat returns a chat's live turn loop, so the client can stream it (lazily spun).
	OpenChat(ws, id string) (Runner, bool)
	// RenameChat / DeleteChat mutate a chat; false for an unknown ws.
	RenameChat(ws, id, name string) bool
	DeleteChat(ws, id string) bool

	// WatchActivity subscribes to background-chat activity across ALL workspaces, so the
	// client can badge chats it hasn't opened without polling. The returned func closes the
	// stream. A workspace's OPEN chat still streams its full events separately — activity is
	// only the lightweight "something changed" signal for the others.
	WatchActivity() (<-chan ChatActivity, func())
}

// ChatActivity is the lightweight badge signal for a chat the client may not have open:
// which chat, and what happened. It carries no conversation content — the client refreshes
// the real state (counts, messages) with listChats / openChat when it wants it.
type ChatActivity struct {
	WS   string `json:"ws"`
	ID   string `json:"id"`
	Kind string `json:"kind"` // one of the Activity* kinds below
}

// Activity kinds. A background chat finished a turn (badge it), or it is waiting on an
// approval (actionable — surface it, not just a dot). Producers (the chat manager) must use
// these exact strings.
const (
	ActivityTurnEnd         = "turnEnd"
	ActivityApprovalPending = "approvalPending"
)

// ChatMeta is one chat's summary for the app's chat list.
type ChatMeta struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Updated string `json:"updated"` // RFC3339
	Turns   int    `json:"turns"`
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
