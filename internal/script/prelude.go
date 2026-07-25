package script

import _ "embed"

// preludeJS is the guest prelude — a small hand-written set of familiar Web/Node shims (btoa,
// TextEncoder, URL, fetch, fs, …) prepended to every JS program before evaluation. See prelude.js for
// the contract and the security note: it runs inside the sandbox with no authority beyond guest code.
//
//go:embed prelude.js
var preludeJS string

// Prelude returns the JS prelude to prepend to a guest program (both the code_run script and every JS
// plugin). The outward-facing APIs it defines bottom out at nocturn.call, so the gate + HITL still
// judge every action — the prelude is DevEx sugar, not a security boundary.
func Prelude() string { return preludeJS }

// withPrelude prepends the prelude to source. A trailing newline keeps the program's own line numbers
// offset by exactly the prelude's line count.
func withPrelude(source string) string {
	return preludeJS + "\n" + source
}
