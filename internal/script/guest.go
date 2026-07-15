package script

import (
	_ "embed"

	"github.com/efuturetoday/nocturn/internal/tool"
)

// interpreterGuest is the QuickJS interpreter compiled to a WASI command
// (quickjs-ng core + internal/script/qjs/nocturn-qjs.c), embedded so Nocturn
// stays a single binary. It declares exactly one host import — nocturn.call —
// and exports malloc/free for the packed-ptr response ABI. Rebuild it only when
// the C shim changes (see internal/script/qjs/build.sh).
//
//go:embed qjs/nocturn-qjs.wasm
var interpreterGuest []byte

// New builds a Runner over the embedded QuickJS interpreter and the given shared
// dispatch Registry (the same one the model dispatches through, so script effects
// are gated and observed identically). This is the ready-to-use constructor; use
// NewWithGuest only to supply a different guest (e.g. in tests).
func New(reg *tool.Registry) *Runner {
	return NewWithGuest(interpreterGuest, reg)
}

// InterpreterGuest returns the embedded QuickJS interpreter wasm — the shared JS
// runtime the plugin layer uses to run a plugin.js without a per-plugin build.
// The returned bytes are read-only; do not mutate.
func InterpreterGuest() []byte { return interpreterGuest }
