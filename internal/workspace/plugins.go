package workspace

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/plugin"
)

// installPlugins discovers the plugins under <dir>/plugins and folds each one's tools into the
// workspace toolset (as top-level <plugin>_<tool> tools, refusing a name collision). A plugin's guest
// can only dispatch to the base tools its manifest lists — its cage — and every action it takes is
// gated the same way the model's own calls are.
//
// What it does NOT do is bind credentials. A plugin declares them exactly as a skill or an MCP server
// does (extension.Decl), and every kind's declaration is registered in one place, by bindExtensions —
// so "the credential goes away when the thing that declared it does" is one rule with one
// implementation rather than a promise each kind keeps on its own.
//
// It returns the plugins it installed — held so the snapshot can close their guests when it retires,
// and so the UI can name them rather than count them.
func (p pass) installPlugins(base, toolset agentkit.ToolSet) ([]*plugin.Plugin, error) {
	plugins := plugin.Discover(filepath.Join(p.dir, extension.Dir), base, p.diag)

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
			// The toolset is this pass's own map, built fresh and published only on success, so
			// writing into it early costs nothing if the pass then fails.
			toolset[n] = t
		}
		installed = append(installed, pl)
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
