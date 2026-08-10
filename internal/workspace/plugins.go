package workspace

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// installPlugins discovers the plugins under <dir>/plugins and folds each one's tools into the
// workspace toolset (as top-level <plugin>_<tool> tools, refusing a name collision), then binds its
// declared credentials host-side on the injector under the plugin's owner. A plugin's guest can only
// dispatch to the base tools its manifest lists — its cage — and every action it takes is gated the
// same way the model's own calls are. A credential value lives in the vault under the (lowercased)
// credential name; a missing value simply means the plugin runs unauthenticated.
// It returns the plugins it installed — held so the snapshot can close their guests when it retires,
// and so the UI can name them rather than count them.
//
// Bindings are RECONCILED rather than added, because this runs again on every reload. Two failures
// hide in "just add them again": AddBinding appends, so a plugin's credential would be injected once
// more per reload; and a plugin DELETED from disk is not in the discovery result at all, so clearing
// only what was found would leave its binding riding along on every request to its host for the life
// of the process — authority outliving the thing it was granted to. prev is therefore the previous
// snapshot's plugins, and every one of their owners is cleared before the current set is bound.
// RemoveBindingsFor also forgets the sources those bindings referenced, so an uninstall drops the
// in-memory credential material too — the behaviour internal/secret documents it for.
func (p pass) installPlugins(base, toolset agentkit.ToolSet, prev []*plugin.Plugin) ([]*plugin.Plugin, error) {
	plugins := plugin.Discover(filepath.Join(p.dir, "plugins"), base, p.diag)
	if p.injector != nil {
		for _, old := range prev {
			p.injector.RemoveBindingsFor(plugin.Owner(old.Name()))
		}
	}
	var installed []*plugin.Plugin
	for _, pl := range plugins.All() {
		pts, err := pl.Tools()
		if err != nil {
			return nil, err
		}
		for _, t := range pts {
			n := t.Spec().Name
			if _, dup := toolset[n]; dup {
				return nil, fmt.Errorf("plugin %q tool %q collides with an existing tool", pl.Name(), n)
			}
			toolset[n] = t
		}
		installed = append(installed, pl)
		if p.injector != nil {
			owner := plugin.Owner(pl.Name())
			p.injector.RemoveBindingsFor(owner)
			for _, c := range pl.Credentials() {
				p.injector.AddBinding(owner, secret.Binding{
					Secret: plugin.SecretName(pl.Name(), c.Name), // owner-namespaced: no cross-plugin key collision
					Host:   c.Host,
					Header: c.Header,
					Prefix: c.Prefix,
				})
			}
		}
	}
	return installed, nil
}

// pluginNames lists installed plugins by name, sorted — the form Inventory reports.
func pluginNames(ps []*plugin.Plugin) []string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name())
	}
	slices.Sort(names)
	return names
}
