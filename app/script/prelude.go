package script

import _ "embed"

// runtimeJS is the guest runtime prelude — a small hand-written set of familiar
// Web/Node shims (btoa, TextEncoder, URL, fetch, fs, …) prepended to every JS
// program before evaluation. See runtime.js for the contract and the security
// note: it runs inside the sandbox with no authority beyond guest code.
//
//go:embed runtime.js
var runtimeJS string

// Prelude returns the JS runtime shim to prepend to a guest program (both the
// code_run script and every plugin). The outward-facing APIs it defines bottom
// out at nocturn.call, so the broker + HITL still gate every action — the prelude
// is DevEx sugar, not a security boundary.
func Prelude() string { return runtimeJS }

// withPrelude prepends the runtime to source. A trailing newline keeps the
// program's own line numbers offset by exactly the prelude's line count.
func withPrelude(source string) string {
	return runtimeJS + "\n" + source
}
