package workspace

import (
	"slices"
	"sync"

	"github.com/efuturetoday/nocturn/internal/memory"
)

// Inventory is what this workspace can do right now: its MCP servers and how they fared, the
// extensions it loaded, and every tool the model can reach.
//
// It is DERIVED on every call, never stored. A stored copy would be a cache with no invalidation,
// and the first thing that makes discovery dynamic — a plugin reconcile that adds or drops tools, an
// MCP server that reconnects, a skill hot-reload — would leave it quietly describing a workspace
// that no longer exists. The state lives with the subsystem that owns it; this only reads it.
type Inventory struct {
	MCP     []MCPStatus
	Plugins []string // tool-providing plugins, by name
	Skills  []string // skills the model can load, by name
	Tools   []string // every tool in the workspace toolset, including those the two above added
}

// MCPStatus is one declared MCP server and what became of it. A server that failed is kept, with
// the reason: the absence of a server you configured is exactly what you opened this view to find,
// and a list that silently omits it cannot tell you.
type MCPStatus struct {
	Name  string
	URL   string
	State MCPState
	Tools int    // tools it contributed; 0 unless connected
	Note  string // why it is not connected, in the words the log used
}

// MCPState is how far a server got.
type MCPState string

const (
	MCPConnected MCPState = "connected"
	// MCPNeedsAuth is not a failure but an errand: the server speaks OAuth and nobody has run
	// `nocturn auth <name>` yet.
	MCPNeedsAuth MCPState = "needs auth"
	MCPFailed    MCPState = "failed"
)

// capabilities is the workspace's current discovery state — the one place a reconciler writes and
// the inventory reads. It is guarded because those are different goroutines the moment anything
// re-discovers on a timer, the way the knowledge index already does.
//
// Open fills it once. Nothing re-fills it yet; the point of the type is that when something does,
// there is exactly one writer to change and no second copy to forget.
type capabilities struct {
	mu      sync.RWMutex
	mcp     []MCPStatus
	plugins []string
	skills  []string
}

func (c *capabilities) set(mcp []MCPStatus, plugins, skills []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mcp, c.plugins, c.skills = mcp, plugins, skills
}

// snapshot copies, so a reader can range over the result while a reconcile replaces the originals.
func (c *capabilities) snapshot() (mcp []MCPStatus, plugins, skills []string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.mcp), slices.Clone(c.plugins), slices.Clone(c.skills)
}

// Inventory reports what the workspace can do, read fresh.
//
// The toolset is read straight from the field: a ToolSet is immutable by construction, so a future
// reconcile REPLACES w.tools rather than mutating it, and this sees the new one on the next call.
// That swap is the one thing such a change has to make safe for concurrent readers — this method is
// one of them.
func (w *Workspace) Inventory() Inventory {
	mcp, plugins, skills := w.caps.snapshot()
	return Inventory{MCP: mcp, Plugins: plugins, Skills: skills, Tools: toolNames(w.tools)}
}

// Memory is the catalog of the assistant's notes — the block folded into every prompt. Empty when
// there are no notes.
func (w *Workspace) Memory() string {
	if w.mem == nil {
		return ""
	}
	return w.mem.Index()
}

// MemoryBudget is how many bytes of the catalog reach the system prompt. The ceiling is enforced —
// entries past it are dropped — so how close a workspace is to it is something a person needs to be
// able to see, and this is what lets a client show it.
func (w *Workspace) MemoryBudget() int { return memory.IndexBudget }

// Documents reports the indexed corpus: how many files, how many chunks. ok is false when no
// embedder is configured, which is a different thing from an empty corpus.
func (w *Workspace) Documents() (files, chunks int, ok bool) {
	if w.knowledge == nil {
		return 0, 0, false
	}
	files, chunks, err := w.knowledge.Stats()
	return files, chunks, err == nil
}

// DocumentPaths lists the indexed files.
func (w *Workspace) DocumentPaths() []string {
	if w.knowledge == nil {
		return nil
	}
	paths, err := w.knowledge.Paths()
	if err != nil {
		return nil
	}
	return paths
}

// Secrets lists the NAMES of the credentials this workspace holds, sorted. Never the values — the
// same rule the sandbox runs on, applied to the screen: that a credential exists is information a
// person needs, what it says is not.
func (w *Workspace) Secrets() []string {
	if w.vault == nil {
		return nil // the vault is locked; nothing is known, not even the names
	}
	names := w.vault.Store().Names()
	slices.Sort(names)
	return names
}

// VaultLocked reports whether the credential vault is sealed. A locked vault is why secrets and MCP
// OAuth are unavailable, and saying so beats showing an empty list.
func (w *Workspace) VaultLocked() bool { return w.vault == nil }
