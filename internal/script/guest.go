package script

import (
	"context"
	_ "embed"
	"sync"

	"github.com/efuturetoday/nocturn/internal/sandbox"
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

// InterpreterEngine is the process-wide QuickJS engine: the ~1.2 MB interpreter
// wasm compiled exactly ONCE and then shared, concurrently, by every code.run and
// every JS plugin. Compilation is ~97% of a cold call, so sharing the compiled
// module is the whole performance win.
//
// sync.OnceValues compiles on first use (not at package init, so an unused binary
// pays nothing) and is safe for concurrent callers: the first triggers the
// compile while the rest block, and all receive the same engine. A compile failure
// is cached — the embedded wasm is deterministic, so a failure is a build bug, not
// a transient error worth retrying.
var InterpreterEngine = sync.OnceValues(func() (*sandbox.Engine, error) {
	return sandbox.NewEngine(context.Background(), interpreterGuest, sandbox.EngineConfig{
		HostNames: []string{gateName},
	})
})

// New builds a Runner over the shared QuickJS interpreter engine and the given
// shared dispatch Registry (the same one the model dispatches through, so script
// effects are gated and observed identically). This is the only constructor: a
// Runner always drives the interpreter (and always prepends the runtime prelude).
// A nil Registry yields an empty one (every effect reports "unknown tool"). The
// engine is compiled lazily on the first Run, not here.
func New(reg *tool.Registry) *Runner {
	if reg == nil {
		reg = tool.NewRegistry(nil)
	}
	return &Runner{Registry: reg}
}

// InterpreterGuest returns the embedded QuickJS interpreter wasm. It is used by
// benchmarks and interpreter tooling (e.g. measuring raw compile cost or a Wizer
// prelude snapshot); production JS execution goes through InterpreterEngine. The
// returned bytes are read-only; do not mutate.
func InterpreterGuest() []byte { return interpreterGuest }
