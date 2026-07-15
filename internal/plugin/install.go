package plugin

import (
	"fmt"
	"sync"

	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Host installs and uninstalls plugins into the shared stack. It reuses the SAME
// Registry and Injector the model uses — a plugin is just more tools on the same
// gated path. Install does NOT grant any effect authority: it only makes the
// plugin's tools available and binds its host-owned credentials. Every effect the
// plugin later performs is still bounded by its ceiling (carried in ctx) and gated
// by the broker + HITL + the user's context grants — installing ≠ silently
// allowing.
type Host struct {
	Registry *tool.Registry
	Injector *secret.Injector // may be nil (no credential binding)

	mu     sync.Mutex
	active map[string]*installed
}

type installed struct {
	tools []string // namespaced tool names, for uninstall
}

// NewHost builds a plugin host over the shared registry and injector.
func NewHost(reg *tool.Registry, inj *secret.Injector) *Host {
	return &Host{Registry: reg, Injector: inj, active: map[string]*installed{}}
}

// Install reviews and installs a loaded plugin. approve is called with the
// manifest (the ceiling + credentials the operator sees) and returns true to
// proceed — the caller decides HOW to review (a terminal prompt, a stored
// manifest-hash approval, ntfy, …), keeping the host decoupled from the channel.
// On approval the plugin's tools are registered and its credentials bound; on
// decline nothing is installed.
func (h *Host) Install(l Loaded, approve func(Manifest) (bool, error)) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	name := l.Manifest.Name
	if _, ok := h.active[name]; ok {
		return fmt.Errorf("plugin %q already installed", name)
	}
	p := New(l, h.Registry)
	tools := p.Tools()
	for _, t := range tools { // reject collisions before touching anything
		if h.Registry.Has(t.Name) {
			return fmt.Errorf("plugin %q tool %q collides with an existing tool", name, t.Name)
		}
	}

	ok, err := approve(l.Manifest)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("plugin %q install declined", name)
	}

	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		h.Registry.Add(t)
		toolNames = append(toolNames, t.Name)
	}
	if h.Injector != nil {
		for _, c := range l.Manifest.Credentials {
			h.Injector.AddBinding(name, secret.Binding{
				Secret: c.Name, Capability: c.Capability, Host: c.Host, Header: c.Header, Prefix: c.Prefix,
			})
		}
	}
	h.active[name] = &installed{tools: toolNames}
	return nil
}

// Uninstall removes a plugin: unregister its tools and drop its credential
// bindings. The user's context/workspace grants (session/always) are untouched —
// they belong to the user, not the plugin, and survive a reinstall.
func (h *Host) Uninstall(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	inst, ok := h.active[name]
	if !ok {
		return fmt.Errorf("plugin %q not installed", name)
	}
	for _, t := range inst.tools {
		h.Registry.Remove(t)
	}
	if h.Injector != nil {
		h.Injector.RemoveBindingsFor(name)
	}
	delete(h.active, name)
	return nil
}

// Installed reports the names of currently installed plugins.
func (h *Host) Installed() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.active))
	for n := range h.active {
		names = append(names, n)
	}
	return names
}
