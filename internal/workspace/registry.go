package workspace

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/discovery"
)

// trashDir is where a deleted workspace goes. It is a dot-directory so the registry's own scan skips
// it — see NewRegistry.
const trashDir = ".trash"

// Registry is the daemon's set of open workspaces, and the one place a workspace comes into or goes
// out of existence while the process runs.
//
// It exists because the set used to be a plain map built once at startup and read without
// synchronisation. That was true enough while the only way to add a workspace was to create a folder
// and restart; it stops being true the moment a phone can. Every read goes through here, and Create
// and Delete are the only writers.
//
// OnOpen/OnClose are the seam the daemon hangs its per-workspace wiring on — chat activity, the event
// stream, reminders, notifications, the agent schedulers. Registering it here rather than in a startup
// loop is what makes a workspace created at runtime indistinguishable from one that was there all
// along.
type Registry struct {
	host Host
	root string

	mu     sync.RWMutex
	spaces map[string]*Workspace
	// creating holds the names being opened right now. It is separate from spaces on purpose: a
	// placeholder in spaces would be a nil *Workspace that Get reports as FOUND, and the caller
	// dereferences what Get hands back. One device creating "work" while another sends a command
	// naming it would then be a nil-pointer panic reachable over the wire. Reserving the name here
	// instead means a reader sees the workspace or does not see it, never a hole.
	creating map[string]struct{}

	onOpen  func(*Workspace)
	onClose func(*Workspace)
}

// NewRegistry opens every workspace under root — each subdirectory is one, by name — and always
// includes DefaultWorkspace so a fresh install and the terminal have somewhere to be.
//
// Dot-directories are skipped, which is not a detail: Delete moves a workspace into <root>/.trash,
// and without the skip the next start would open the trash as a workspace, complete with its own
// vault and schedulers.
func NewRegistry(h Host, root string) (*Registry, error) {
	r := &Registry{host: h, root: root, spaces: map[string]*Workspace{}}

	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ws, err := Open(h, e.Name(), filepath.Join(root, e.Name()))
		if err != nil {
			return nil, err
		}
		r.spaces[e.Name()] = ws
	}
	if _, ok := r.spaces[DefaultWorkspace]; !ok {
		ws, err := Open(h, DefaultWorkspace, filepath.Join(root, DefaultWorkspace))
		if err != nil {
			return nil, err
		}
		r.spaces[DefaultWorkspace] = ws
	}
	return r, nil
}

// OnOpen and OnClose register what the daemon does around a workspace's lifetime. Set once, at wiring
// time, before serving: NewRegistry has already opened what was on disk, so the caller runs the hook
// over Snapshot itself and Create takes it from there.
func (r *Registry) OnOpen(fn func(*Workspace))  { r.onOpen = fn }
func (r *Registry) OnClose(fn func(*Workspace)) { r.onClose = fn }

// Get resolves a workspace by name.
func (r *Registry) Get(name string) (*Workspace, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ws, ok := r.spaces[name]
	return ws, ok
}

// Names lists the open workspaces, sorted — a map has no order, and a list that reshuffles itself
// between two looks is unreadable.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Sorted(maps.Keys(r.spaces))
}

// Snapshot copies the open workspaces, so a caller can range over the result while another creates or
// deletes one.
func (r *Registry) Snapshot() []*Workspace {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Collect(maps.Values(r.spaces))
}

// Len reports how many workspaces are open.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.spaces)
}

// Create opens a new workspace named name, with an optional display title.
//
// The name is checked against discovery.ValidName — the same rule plugins, MCP servers and agents
// answer to — because it is not a label: it becomes a directory, the input to this workspace's vault
// and shard keys, and the ws field on every wire command and log line. The title is set in the same
// call rather than afterwards, so no client ever sees the raw name in the moment between.
func (r *Registry) Create(name, title string) (*Workspace, error) {
	if !discovery.ValidName(name) {
		return nil, fmt.Errorf(
			"workspace name %q: must be lowercase letters, digits, - or _, starting with a letter or digit", name)
	}

	r.mu.Lock()
	// Lazily, so the zero Registry is usable rather than a nil-map panic on first write.
	if r.spaces == nil {
		r.spaces = map[string]*Workspace{}
	}
	if r.creating == nil {
		r.creating = map[string]struct{}{}
	}
	_, taken := r.spaces[name]
	_, inFlight := r.creating[name]
	if taken || inFlight {
		r.mu.Unlock()
		return nil, fmt.Errorf("workspace %q already exists", name)
	}
	// Reserved while holding the lock, so two devices creating the same name cannot both reach Open —
	// which would open one directory twice, with two vaults and two sets of timers on the same files.
	r.creating[name] = struct{}{}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.creating, name)
		r.mu.Unlock()
	}()

	// Opened OUTSIDE the lock: it does vault work and MCP handshakes, seconds of it, and every reader
	// would be waiting on a workspace they are not even asking about.
	ws, err := Open(r.host, name, filepath.Join(r.root, name))
	if err != nil {
		return nil, err
	}
	if title != "" {
		if err := ws.SetTitle(title); err != nil {
			ws.log.Warn("workspace created but its title could not be written", "err", err)
		}
	}

	r.mu.Lock()
	r.spaces[name] = ws
	r.mu.Unlock()

	if r.onOpen != nil {
		r.onOpen(ws)
	}
	return ws, nil
}

// Delete closes a workspace and moves its directory into <root>/.trash/<name>-<unix>.
//
// A rename, not a removal. What is in there is every conversation, every note the assistant kept, and
// a vault — and the person deleting it is doing so from a phone, on a list, possibly by accident.
// Recovering is then `mv`; recovering from os.RemoveAll is nothing.
//
// DefaultWorkspace cannot be deleted: NewRegistry recreates it on the next start, so the operation
// would appear to work and then undo itself. Saying so is better than doing that.
func (r *Registry) Delete(name string) error {
	if name == DefaultWorkspace {
		return fmt.Errorf("workspace %q cannot be deleted — it is the default and is recreated at startup", name)
	}

	r.mu.Lock()
	ws, ok := r.spaces[name]
	delete(r.spaces, name)
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("no workspace %q", name)
	}

	// Out of the registry first, then closed, then moved: no command can resolve it any more, its
	// sessions and timers are stopped, and only then does the directory move under them.
	if r.onClose != nil {
		r.onClose(ws)
	}
	ws.Close()

	trash := filepath.Join(r.root, trashDir)
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return err
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	return os.Rename(filepath.Join(r.root, name), filepath.Join(trash, name+"-"+stamp))
}

// SetTitle renames a workspace for human eyes. See Workspace.SetTitle for why that is not the folder.
func (r *Registry) SetTitle(name, title string) error {
	ws, ok := r.Get(name)
	if !ok {
		return fmt.Errorf("no workspace %q", name)
	}
	return ws.SetTitle(title)
}

// Close closes every open workspace. Call once, on daemon shutdown.
func (r *Registry) Close() {
	for _, ws := range r.Snapshot() {
		ws.Close()
	}
}
