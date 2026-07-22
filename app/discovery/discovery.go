// Package discovery holds the vocabulary every discoverable kind shares (agents,
// skills, plugins, MCP servers): how a skipped item is recorded, and how an
// item's name is resolved. One implementation so all four kinds behave
// identically instead of hand-copying the rule four times and drifting.
package discovery

import "github.com/efuturetoday/nocturn/agentkit"

// Diagnose records one skipped or shadowed item into the collector if present
// (nil-safe: some callers discover without one). A discovery skip is non-fatal —
// a Warn, not an Error — because the item's authority is then simply absent
// (fail-closed), and the rest still loads.
func Diagnose(diag *agentkit.Diagnostics, subject, msg string) {
	if diag != nil {
		diag.Warn(subject, msg)
	}
}

// ResolveName picks an item's name under one rule for every kind: identity is the
// folder or file name (fsName); a manifest MAY override it with field, and when
// present the field wins — this lets a skill keep its canonical agentskills.io
// name when vendored under a different folder. A field that disagrees with fsName
// is a Warn (loaded anyway) so accidental drift on the kinds that never need it
// (agents, plugins, servers) is visible instead of silent.
func ResolveName(diag *agentkit.Diagnostics, kind, fsName, field string) string {
	if field == "" {
		return fsName
	}
	if field != fsName {
		Diagnose(diag, kind+":"+field, "name "+field+" differs from its folder/file "+fsName+" (using "+field+")")
	}
	return field
}
