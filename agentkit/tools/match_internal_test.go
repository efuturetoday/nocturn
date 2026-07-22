package tools

// Internal (white-box) test: parentDomain is unexported, so per CLAUDE.md §9 it is exercised in the
// package's own test package. Everything else lives in the external tools_test package.

import "testing"

func TestParentDomain_Labels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		host string
		want string
	}{
		{"two labels return themselves", "example.com", "example.com"},
		{"three labels keep last two", "a.example.com", "example.com"},
		{"four labels keep last two", "b.a.example.com", "example.com"},
		{"single label has no parent", "localhost", ""},
		{"empty has no parent", "", ""},
		{"port travels with the last label", "a.example.com:8080", "example.com:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parentDomain(tt.host); got != tt.want {
				t.Errorf("parentDomain(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}
