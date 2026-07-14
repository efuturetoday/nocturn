package script

import (
	_ "embed"

	"github.com/efuturetoday/nocturn/internal/brain"
)

// interpreterGuest is the QuickJS interpreter compiled to a WASI command
// (quickjs-ng core + internal/script/qjs/nocturn-qjs.c), embedded so Nocturn
// stays a single binary. It declares exactly one host import — nocturn.call —
// and exports malloc/free for the packed-ptr response ABI. Rebuild it only when
// the C shim changes (see internal/script/qjs/build.sh).
//
//go:embed qjs/nocturn-qjs.wasm
var interpreterGuest []byte

// NewRunner builds a Runner over the embedded QuickJS interpreter and the given
// shared dispatch Registry (the same one the model dispatches through, so script
// effects are gated and observed identically).
func NewRunner(reg *brain.Registry) *Runner {
	return New(interpreterGuest, reg)
}
