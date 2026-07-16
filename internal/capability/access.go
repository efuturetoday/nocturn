package capability

import "fmt"

// ParseAccess maps an author-declared access list to a Match — the one place the
// read/write vocabulary crosses from config (plugin.json cage entries, agent
// frontmatter) into the broker's write axis. "read"+"write" → MatchAny, "read" →
// MatchRead, "write" → MatchWrite. An empty list yields MatchNone (the fail-closed
// zero — grants nothing); the caller decides whether that is an error (a cage entry
// must name its access) or a permissive default (a deny rule with no access denies
// both). An unknown token is a hard error so a typo never silently widens reach.
func ParseAccess(access []string) (Match, error) {
	var read, write bool
	for _, a := range access {
		switch a {
		case "read":
			read = true
		case "write":
			write = true
		default:
			return MatchNone, fmt.Errorf("access must be read or write (got %q)", a)
		}
	}
	switch {
	case read && write:
		return MatchAny, nil
	case read:
		return MatchRead, nil
	case write:
		return MatchWrite, nil
	default:
		return MatchNone, nil // empty — caller decides
	}
}
