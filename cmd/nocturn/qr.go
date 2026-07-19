package main

import (
	"strings"

	"rsc.io/qr"
)

// renderQR returns an ANSI rendering of text as a QR code, scannable from a phone camera
// pointed at the terminal. Two vertical modules share one character cell via the upper
// half-block ▀ (its foreground paints the top module, its background the bottom), so the code
// stays roughly square instead of doubly tall. A light quiet zone borders it, as scanners
// require. Colors are set explicitly (black modules on white) so it reads the same on a light
// or dark terminal.
func renderQR(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 2 // modules of light border on every side
	size := code.Size

	var b strings.Builder
	for y := -quiet; y < size+quiet; y += 2 {
		for x := -quiet; x < size+quiet; x++ {
			fg, bg := "97", "107" // white foreground/background = a light module
			if isDark(code, x, y) {
				fg = "30" // black = a dark module (top half)
			}
			if isDark(code, x, y+1) {
				bg = "40" // black = a dark module (bottom half)
			}
			b.WriteString("\x1b[")
			b.WriteString(fg)
			b.WriteByte(';')
			b.WriteString(bg)
			b.WriteString("m▀")
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String(), nil
}

// isDark reports whether module (x,y) is a dark cell. Coordinates in the quiet zone (outside
// the code) are light.
func isDark(code *qr.Code, x, y int) bool {
	if x < 0 || y < 0 || x >= code.Size || y >= code.Size {
		return false
	}
	return code.Black(x, y)
}
