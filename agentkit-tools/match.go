package tools

import (
	"strings"

	"github.com/efuturetoday/agentkit-gate"
)

// HostMatch is a gate.Matcher for host targets: a grant pattern "*.example.com" covers example.com
// and any subdomain of it; "*" covers any host; otherwise the host must match exactly. http_get
// passes it to gate.Check for the net axis. Host semantics live here — in the network-tool module —
// not in the general gate library.
func HostMatch(pattern, host string) bool {
	if pattern == "*" || pattern == host {
		return true
	}
	if base, ok := strings.CutPrefix(pattern, "*."); ok {
		return host == base || strings.HasSuffix(host, "."+base)
	}
	return false
}

// HostSuggestions returns the sensible grant WIDENINGS to offer a human approving a host: the parent
// domain wildcard (*.example.com) for a subdomain. Exact-host and "any" are not suggested — the
// approver always offers the exact host, and the tool only nudges the one reasonable widening.
func HostSuggestions(host string) []gate.Grant {
	if d := parentDomain(host); d != "" && d != host {
		return []gate.Grant{{Kind: NetAxis, Target: "*." + d}}
	}
	return nil
}

func parentDomain(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return ""
}
