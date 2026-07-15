package plugin

import (
	"fmt"
	"strings"
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

// Owner is the credential-injection owner id for a plugin: "plugin:<name>". The
// typed prefix means plugins, and later other injecting owners (a remote-MCP
// connection would be "mcp:<server>"), share ONE owner namespace without
// colliding — a plugin "github" and an MCP server "github" get distinct owners.
func Owner(name string) string { return "plugin:" + name }

// SecretName is the secret-store/vault key for a plugin credential, bound to the
// owner, the credential name, AND the host it is issued for:
// "<owner>/<credential>@<host>" (host lowercased). Owner-scoping already stops a
// plugin from referencing another owner's credential by a bare name; host-binding
// adds a second boundary: if a plugin's manifest is edited to point the SAME
// credential at a DIFFERENT host, the key changes, so the stored token is not
// found and the operator must re-authorize — a token issued for host A can never
// be injected to host B under a reused name (no silent cross-host exfil). The
// OAuth wiring registers the source under the same key.
func SecretName(owner, credential, host string) string {
	return owner + "/" + credential + "@" + strings.ToLower(host)
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
		owner := Owner(name)
		for _, c := range l.Manifest.Credentials {
			h.Injector.AddBinding(owner, secret.Binding{
				Secret: SecretName(owner, c.Name, c.Host), Capability: c.Capability, Host: c.Host, Header: c.Header, Prefix: c.Prefix,
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
		h.Injector.RemoveBindingsFor(Owner(name))
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
