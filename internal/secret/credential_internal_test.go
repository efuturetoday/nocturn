package secret

import "testing"

func TestHostMatches(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"graph.microsoft.com", "graph.microsoft.com", true}, // exact
		{"graph.microsoft.com", "evil.com", false},           // different host
		{"*.example.com", "a.example.com", true},             // sub-domain
		{"*.example.com", "a.b.example.com", true},           // deeper sub-domain
		{"*.example.com", "example.com", false},              // bare domain not covered
		{"*.example.com", "notexample.com", false},           // suffix confusion guarded
		{"*.example.com", "evil.example.com.attacker.io", false},
		{"", "example.com", false},  // empty pattern matches nothing (fail closed)
		{"*", "example.com", false}, // bare "*" matches nothing (no all-hosts credential)
		{"example.com", "", false},  // empty host never matches
	}
	for _, c := range cases {
		if got := hostMatches(c.pattern, c.host); got != c.want {
			t.Errorf("hostMatches(%q, %q) = %v, want %v", c.pattern, c.host, got, c.want)
		}
	}
}
