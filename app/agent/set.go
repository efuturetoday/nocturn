package agent

import "sort"

// Set is an immutable collection of declared agents, keyed by name (mirrors agentkit's ToolSet /
// SkillSet). Build one with Discover; read it with Get and All.
type Set map[string]Agent

// Get returns the named agent.
func (s Set) Get(name string) (Agent, bool) {
	a, ok := s[name]
	return a, ok
}

// All returns the agents sorted by name.
func (s Set) All() []Agent {
	out := make([]Agent, 0, len(s))
	for _, a := range s {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
