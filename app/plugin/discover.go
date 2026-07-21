package plugin

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/efuturetoday/nocturn/agentkit"
)

// LoadAll discovers and builds every plugin under root — each subdirectory holding a plugin.json —
// each caged to a subset of the base tools per its manifest. A missing root is not an error (simply
// no plugins). A malformed plugin is, so a broken install surfaces at startup rather than silently
// vanishing. The caller registers each plugin's tools and credential bindings.
func LoadAll(root string, base agentkit.ToolSet) ([]*Plugin, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Plugin
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		l, err := Load(filepath.Join(root, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("plugin %q: %w", e.Name(), err)
		}
		out = append(out, New(l, base))
	}
	return out, nil
}
