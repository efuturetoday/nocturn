package agent_test

import (
	"testing"

	"github.com/efuturetoday/nocturn/internal/agent"
)

func TestAgent_Matches_ExactAndGroupSeparators(t *testing.T) {
	t.Parallel()

	a := agent.Agent{Name: "researcher", Tools: []string{"http", "file_read"}}

	tests := []struct {
		name string
		tool string
		want bool
	}{
		{"exact group name", "http", true},
		{"underscore member", "http_read", true},
		{"underscore member 2", "http_write", true},
		{"dot member", "http.read", true},
		{"slash member", "http/get", true},
		{"exact non-group tool", "file_read", true},
		{"member of a member", "file_read.stat", true},
		{"same prefix, no separator (httpfoo)", "httpfoo", false},
		{"same prefix, wrong separator (https_x)", "https_x", false},
		{"unrelated tool", "dns_resolve", false},
		{"group name is a prefix of the tool but not caged", "file", false},
		{"empty tool name", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := a.Matches(tt.tool); got != tt.want {
				t.Errorf("Matches(%q) = %v, want %v", tt.tool, got, tt.want)
			}
		})
	}
}

func TestAgent_Matches_EmptyTools_MatchesNothing(t *testing.T) {
	t.Parallel()

	a := agent.Agent{Name: "reasoner"} // Tools nil = a pure reasoner
	for _, tool := range []string{"http", "http_read", "file_read", ""} {
		if a.Matches(tool) {
			t.Errorf("Matches(%q) = true on empty toolset, want false", tool)
		}
	}
}
