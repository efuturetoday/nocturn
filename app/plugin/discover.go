package plugin

import (
	"os"
	"path/filepath"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/discovery"
)

// Discover builds every plugin under root — each subdirectory holding a plugin.json — into a Set,
// each caged to a subset of the base tools per its manifest. A missing root yields an empty Set. A
// malformed plugin is SKIPPED with a diagnostic rather than aborting the scan — a broken plugin's
// tools + credentials are then simply absent (fail-closed), and the other plugins still load. A
// duplicate name keeps the first (shadowing). The caller registers each plugin's tools and
// credential bindings.
func Discover(root string, base agentkit.ToolSet, diag *agentkit.Diagnostics) Set {
	set := Set{}
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			discovery.Diagnose(diag, "plugin", "read dir "+root+": "+err.Error())
		}
		return set
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		l, err := Load(filepath.Join(root, e.Name()))
		if err != nil {
			discovery.Diagnose(diag, "plugin:"+e.Name(), err.Error())
			continue
		}
		// Identity is the folder; a manifest name that overrides it warns (Load already
		// defaulted an omitted name to the folder, so this fires only on an explicit mismatch).
		if l.Manifest.Name != e.Name() {
			discovery.Diagnose(diag, "plugin:"+l.Manifest.Name, "name "+l.Manifest.Name+" differs from its folder "+e.Name()+" (using "+l.Manifest.Name+")")
		}
		p := New(l, base)
		if _, dup := set[p.Name()]; dup {
			discovery.Diagnose(diag, "plugin:"+p.Name(), "skipped (duplicate name; first wins)")
			continue
		}
		set[p.Name()] = p
	}
	return set
}
