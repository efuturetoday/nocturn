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
		// Identity is the FOLDER — it fixes the credential owner + shard key, so a manifest
		// name cannot forge it. Overwrite the (advisory) manifest name with the folder; a
		// mismatch warns. This makes p.Name()/Owner/SecretName/tool-prefix all folder-pinned.
		name, ok := discovery.ResolveName(diag, "plugin", e.Name(), l.Manifest.Name)
		if !ok {
			continue // folder name not a valid identifier
		}
		l.Manifest.Name = name
		p := New(l, base)
		if _, dup := set[p.Name()]; dup {
			discovery.Diagnose(diag, "plugin:"+p.Name(), "skipped (duplicate name; first wins)")
			continue
		}
		set[p.Name()] = p
	}
	return set
}
