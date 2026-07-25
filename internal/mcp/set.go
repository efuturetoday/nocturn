package mcp

import "sort"

// Set is a collection of declared MCP servers, keyed by name (mirrors agentkit's ToolSet / SkillSet
// and agent.Set). Build one with Discover; read it with Get and All. The name is the file's basename
// — the single source of identity — so a Set can never hold two servers under one name.
type Set map[string]Server

// Get returns the named server.
func (s Set) Get(name string) (Server, bool) {
	srv, ok := s[name]
	return srv, ok
}

// All returns the servers sorted by name.
func (s Set) All() []Server {
	out := make([]Server, 0, len(s))
	for _, srv := range s {
		out = append(out, srv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
