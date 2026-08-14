package workspace

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/discovery"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/skill"
)

// foldPluginSkills adds the SKILL.md a plugin bundles to the workspace's skill catalog.
//
// A plugin brings tools; a skill says when to reach for them and what its arguments really take. The
// two travel together and are read in two different places — the tool spec on every turn, the skill
// body only once the model decides it is relevant — so bundling one is how a plugin explains itself
// without paying for the explanation in every prompt.
//
// A hand-written skill WINS a name collision. The precedence is deliberate: a skill under skills/ is
// something this household wrote or installed on purpose, and an installed plugin must not be able to
// take over the name of one by shipping a SKILL.md that claims it. The shadowed one is reported, the
// way every other discovery collision is.
func foldPluginSkills(into agentkit.SkillSet, root string, diag *agentkit.Diagnostics) {
	bodies := plugin.SkillBodies(root)
	// Sorted, because two plugins bundling one skill name would otherwise pick a different winner on
	// every reload — a workspace that answers differently after a restart, for no visible reason.
	for _, folder := range slices.Sorted(maps.Keys(bodies)) {
		body := bodies[folder]
		sk, err := skill.Parse(body, folder)
		if err != nil {
			discovery.Diagnose(diag, "plugin:"+folder, err.Error())
			continue
		}
		if _, taken := into[sk.Name]; taken {
			discovery.Diagnose(diag, "plugin:"+folder,
				fmt.Sprintf("bundled skill %q skipped (a skill of that name is already installed)", sk.Name))
			continue
		}
		into[sk.Name] = sk
	}
}

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
//
// The reconciliation is also STAGED, because the injector is durable while a pass is not. Writing as
// the loop walks the plugins leaves the injector half-reconciled when a later plugin fails — and a
// failed pass publishes no snapshot, so the workspace goes on serving the OLD one against an injector
// that has already lost its bindings. Credential injection would silently stop for plugins that were
// working a second ago. Nothing is written until every plugin has produced its tools and cleared the
// collision check.
func (p pass) installPlugins(base, toolset agentkit.ToolSet, prev []*plugin.Plugin) ([]*plugin.Plugin, error) {
	plugins := plugin.Discover(filepath.Join(p.dir, "plugins"), base, p.diag)

	var installed []*plugin.Plugin
	staged := make(map[string][]secret.Binding)
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
			// The toolset is this pass's own map, built fresh and published only on success, so
			// writing into it early costs nothing if the pass then fails.
			toolset[n] = t
		}
		installed = append(installed, pl)
		owner := plugin.Owner(pl.Name())
		for _, c := range pl.Credentials() {
			staged[owner] = append(staged[owner], secret.Binding{
				Secret: plugin.SecretName(pl.Name(), c.Name), // owner-namespaced: no cross-plugin key collision
				Host:   c.Host,
				Header: c.Header,
				Prefix: c.Prefix,
			})
		}
	}

	if p.injector == nil {
		return installed, nil
	}
	// Past this point nothing can fail, so the injector moves in one step. Every owner from the
	// previous snapshot is cleared — a plugin DELETED from disk is not in this result at all, and
	// clearing only what was found would leave its binding riding along on every request to its host
	// for the life of the process. Every current owner is cleared too, because AddBinding appends and
	// a reload would otherwise inject the same credential once more per pass. RemoveBindingsFor also
	// forgets the sources those bindings referenced, so an uninstall drops the in-memory credential
	// material with them — the behaviour internal/secret documents it for.
	for _, old := range prev {
		p.injector.RemoveBindingsFor(plugin.Owner(old.Name()))
	}
	for _, pl := range installed {
		p.injector.RemoveBindingsFor(plugin.Owner(pl.Name()))
	}
	for owner, bindings := range staged {
		for _, b := range bindings {
			p.injector.AddBinding(owner, b)
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
