package main

import (
	"fmt"
	"strings"

	"github.com/efuturetoday/nocturn/internal/skill"
)

// reportSkills prints a one-time summary of the discovered skills and any
// discovery diagnostics (skipped/shadowed skills), before the TUI takes the
// terminal. Unlike a plugin install, this NEVER prompts: a skill carries zero
// authority (it only steers the model; every effect still passes the broker +
// HITL), so surfacing it is informational, not a trust decision. Silent when
// there is nothing to report.
func reportSkills(ix *skill.Index) {
	if ix.Len() == 0 && len(ix.Diags) == 0 {
		return
	}
	if ix.Len() > 0 {
		names := make([]string, 0, ix.Len())
		for _, s := range ix.Skills() {
			names = append(names, s.Name)
		}
		fmt.Printf("Skills: %d — %s (type /skills to list)\n", ix.Len(), strings.Join(names, ", "))
	}
	for _, d := range ix.Diags {
		fmt.Printf("  [%s] %s\n", d.Level, d.Message)
	}
}
