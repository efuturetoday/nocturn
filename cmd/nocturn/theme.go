package main

import "github.com/charmbracelet/lipgloss"

// The Nocturn palette — nocturnal indigo/violet, adaptive to a light or dark
// terminal (lipgloss.AdaptiveColor picks per background), so the hand-rolled
// chrome harmonizes with glamour's own auto-themed markdown body instead of
// fighting it. One semantic token set, no raw ANSI scattered through the UI.
var (
	cAccent       = lipgloss.AdaptiveColor{Light: "61", Dark: "141"}  // indigo/violet — the identity
	cAccentBright = lipgloss.AdaptiveColor{Light: "57", Dark: "183"}  // brighter violet — selection
	cUser         = lipgloss.AdaptiveColor{Light: "25", Dark: "111"}  // cool blue — the user's own turns
	cTool         = lipgloss.AdaptiveColor{Light: "244", Dark: "245"} // muted grey — tools stay quiet
	cOk           = lipgloss.AdaptiveColor{Light: "28", Dark: "114"}  // green — success
	cWarn         = lipgloss.AdaptiveColor{Light: "130", Dark: "179"} // amber — awaiting approval
	cErr          = lipgloss.AdaptiveColor{Light: "124", Dark: "174"} // red — errors / denied
	cFaint        = lipgloss.AdaptiveColor{Light: "245", Dark: "240"} // real dim colour (not just Faint)
	cRule         = lipgloss.AdaptiveColor{Light: "250", Dark: "240"} // subtle-but-visible rules / separators
)

var (
	headerNameStyle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	headerModelStyle = lipgloss.NewStyle().Foreground(cFaint)
	ruleStyle        = lipgloss.NewStyle().Foreground(cRule)

	userStyle     = lipgloss.NewStyle().Bold(true).Foreground(cUser)
	assistantMark = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	toolStyle     = lipgloss.NewStyle().Foreground(cTool)
	okStyle       = lipgloss.NewStyle().Foreground(cOk)
	warnStyle     = lipgloss.NewStyle().Foreground(cWarn)
	errStyle      = lipgloss.NewStyle().Foreground(cErr)
	hintStyle     = lipgloss.NewStyle().Foreground(cFaint)

	askStyle      = lipgloss.NewStyle().Bold(true).Foreground(cAccent)       // approval heading
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(cAccentBright) // selected option — distinct from heading

	welcomeTitle = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	welcomeDim   = lipgloss.NewStyle().Foreground(cFaint)
)

// pulsePalette runs indigo → bright violet → indigo, for the live tool-call and
// "thinking" dot. Fixed 256 codes (a small glowing dot), chosen to read on both
// light and dark backgrounds.
var pulsePalette = []string{"60", "62", "98", "135", "177", "135", "98", "62"}
