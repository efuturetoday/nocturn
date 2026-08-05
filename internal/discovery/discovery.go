// Package discovery holds the vocabulary every discoverable kind shares (agents,
// skills, plugins, MCP servers): how a skipped item is recorded, and how an
// item's name is resolved. One implementation instead of hand-copying the rule
// and letting the copies drift.
//
// Name resolution covers three of the four: skills resolve theirs in their own
// loader, field-wins, for the reason given on ResolveName. Skipping is shared by
// all four.
package discovery

import (
	"regexp"

	"github.com/efuturetoday/nocturn/agentkit"
)

// nameRe bounds a discoverable item's name. The name is a SECURITY PRINCIPAL — it
// becomes a credential owner ("plugin:<name>") and a key-derivation component for
// the secret shard — so it must be a tame identifier: no path separators, no ":",
// no NUL, no whitespace, no unicode. It also namespaces model-facing tools
// ("<name>_<tool>"), which OpenAI/agentkit require to match ^[a-zA-Z0-9_-]{1,64}$.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidName reports whether s is a safe item identifier (see nameRe).
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Diagnose records one skipped or shadowed item into the collector if present
// (nil-safe: some callers discover without one). A discovery skip is non-fatal —
// a Warn, not an Error — because the item's authority is then simply absent
// (fail-closed), and the rest still loads.
func Diagnose(diag *agentkit.Diagnostics, subject, msg string) {
	if diag != nil {
		diag.Warn(subject, msg)
	}
}

// ResolveName resolves the name of an owner-bearing item (agent, plugin, MCP
// server) under one secure rule: identity is the FOLDER name (fsName), which the
// operator's filesystem placement fixes and a manifest cannot forge. A manifest
// "name" field is advisory only — if it disagrees with the folder it is a Warn and
// the FOLDER wins, so the credential owner + shard key can never be moved by what
// an artifact claims about itself. The name must be a valid identifier (it is a
// security principal); a folder whose name is not is SKIPPED — ok is false.
//
// Skills are the deliberate exception (zero authority + the agentskills.io standard
// puts the canonical name in SKILL.md): they resolve field-wins in their own loader,
// not here.
func ResolveName(diag *agentkit.Diagnostics, kind, fsName, field string) (string, bool) {
	if !ValidName(fsName) {
		Diagnose(diag, kind+":"+fsName, "invalid name (want "+nameRe.String()+"), skipped")
		return "", false
	}
	if field != "" && field != fsName {
		Diagnose(diag, kind+":"+fsName, "manifest name "+field+" differs from its folder "+fsName+" (the folder wins)")
	}
	return fsName, true
}
