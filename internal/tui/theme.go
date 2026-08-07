package tui

import (
	tui "github.com/grindlemire/go-tui"
)

// The brand's colours, in the one place that knows them.
//
// They are the same values the rest of the product is drawn from: docs/src/styles/brand.css defines
// them, mobile/src/theme/variables.css is generated from it, and these three are that palette's
// accent, its light tint and the text that sits on it. Written as hex rather than as one of the
// terminal's sixteen names on purpose — the sixteen are whatever the user's colour scheme says they
// are, so cyan is a different colour on every machine, and a brand that changes per terminal is not
// one.
//
// A terminal that cannot do truecolour degrades these to its nearest palette entry, which is the
// framework's job and not ours.
// RGBColor and not HexColor: the hex form returns an error, which would make these three either a
// package-level error nobody reads or an init() that can fail. The numbers are the same colours.
var (
	// accent is the gopher purple (#915bd7): the fill behind the thing that is currently selected.
	accent = tui.RGBColor(145, 91, 215)
	// accentHigh is the light tint (#d0b4f9): markers and text that have to READ as chosen rather
	// than be sat on, where a fill would be too heavy.
	accentHigh = tui.RGBColor(208, 180, 249)
	// onAccent is what stays legible on top of accent.
	onAccent = tui.RGBColor(255, 255, 255)
)

// A pane says it holds the keyboard with its own border: square, heavier, and in the brand's purple.
//
// It is drawn by us rather than left to the framework, and that is the whole point. An element with
// a border and no onFocus handler gets the framework's built-in highlight — a rounded box in the
// terminal's own cyan — which is a colour this program does not choose and a shape the rest of the
// UI does not use. Passing the state in and computing the border from it makes the answer ours.
//
// Weight carries the signal and colour confirms it, in that order: a heavier rule is visible in the
// corner of the eye and in a terminal with no truecolour, where a purple would be rounded to
// whatever is nearest.
func paneBorder(focused bool) tui.BorderStyle {
	if focused {
		return tui.BorderThick
	}
	return tui.BorderSingle
}

func paneBorderStyle(focused bool) tui.Style {
	if focused {
		return tui.NewStyle().Foreground(accent)
	}
	return tui.NewStyle().Foreground(tui.BrightBlack)
}
