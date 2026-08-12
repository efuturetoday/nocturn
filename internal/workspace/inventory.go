package workspace

import (
	"slices"

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

// Inventory reports what the workspace can do, read fresh.
//
// Every field comes from ONE snapshot, read once. That is the whole reason discovery state lives
// there rather than in a struct of its own beside the toolset: two reads — the discovery lists from
// one place and the tools from another — could land either side of a reload and report a workspace
// that never existed, with the new tool list against the old MCP list. On the wire this is what a
// device polls right after installing something, so the torn read was not hypothetical.
//
// The slices are cloned because a snapshot is shared: a caller may range over the result for as long
// as it likes without holding anything back from being retired.
func (w *Workspace) Inventory() Inventory {
	a := w.snapshot()
	return Inventory{
		MCP:     slices.Clone(a.mcp),
		Plugins: slices.Clone(a.names.plugins),
		Skills:  slices.Clone(a.names.skills),
		Tools:   toolNames(a.tools),
	}
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
	if w.sec.vault == nil {
		return nil // the vault is locked; nothing is known, not even the names
	}
	names := w.sec.vault.Store().Names()
	slices.Sort(names)
	return names
}

// VaultLocked reports whether the credential vault is sealed. A locked vault is why secrets and MCP
// OAuth are unavailable, and saying so beats showing an empty list.
func (w *Workspace) VaultLocked() bool { return w.sec.vault == nil }
