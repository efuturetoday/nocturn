package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// metaFile holds the parts of a workspace a person edits and discovery does not decide.
const metaFile = "workspace.json"

// meta is the on-disk shape of metaFile. Optional: a workspace without one is named by its folder.
type meta struct {
	Title string `json:"title,omitempty"`
}

// title is the workspace's display name, held apart from its identity.
//
// The two are different things and it is worth being exact about why, because the obvious design —
// rename the folder — is the one that quietly destroys credentials. The folder name is the input to
// every key this workspace has: the vault key (Master.WorkspaceKey), every plugin and MCP shard key
// (Master.ShardKey over the workspace-relative path), and the AAD they are bound to. Renaming the
// folder therefore does not move a workspace, it makes its vault and every shard undecryptable, with
// no error until something reaches for a credential.
//
// So the folder name is identity, forever: the key input, the ws= on every log line, the ws field on
// every wire command, and what discovery.ResolveName says everywhere else in this tree. The title is
// a label over it, freely changeable because nothing depends on it.
type title struct {
	mu    sync.RWMutex
	value string // "" = fall back to the folder name
}

func (t *title) get(fallback string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.value == "" {
		return fallback
	}
	return t.value
}

func (t *title) set(v string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.value = v
}

// Title is what to call this workspace on a screen. It is the folder name until someone sets one.
func (w *Workspace) Title() string { return w.title.get(w.name) }

// SetTitle renames the workspace for human eyes only — the folder, the keys and the wire name are
// untouched. An empty or blank title clears the override, so the folder name shows again.
func (w *Workspace) SetTitle(v string) error {
	v = strings.TrimSpace(v)
	if err := writeMeta(w.dir, meta{Title: v}); err != nil {
		return err
	}
	w.title.set(v)
	return nil
}

// readMeta loads metaFile, tolerantly: absent or unreadable is an empty meta, because a workspace
// missing a label is a workspace that shows its folder name, not a workspace that fails to open.
func readMeta(dir string) meta {
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return meta{}
	}
	var m meta
	if json.Unmarshal(data, &m) != nil {
		return meta{}
	}
	return m
}

// writeMeta persists metaFile atomically (write then rename), 0600 — the same care the grant store
// takes, for the same reason: it sits in the control plane, beside the vault.
func writeMeta(dir string, m meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, metaFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
