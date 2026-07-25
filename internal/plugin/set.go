package plugin

import "sort"

// Set is a collection of installed plugins, keyed by name (mirrors agentkit's ToolSet / SkillSet and
// agent.Set / mcp.Set). Build one with Discover; read it with Get and All.
type Set map[string]*Plugin

// Get returns the named plugin.
func (s Set) Get(name string) (*Plugin, bool) {
	p, ok := s[name]
	return p, ok
}

// All returns the plugins sorted by name.
func (s Set) All() []*Plugin {
	out := make([]*Plugin, 0, len(s))
	for _, p := range s {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
