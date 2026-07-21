// Package script runs untrusted JavaScript on the sandbox interpreter (QuickJS compiled to wasm) and
// bridges a script's actions through a single generic host gate onto the SAME agentkit tools the
// model uses.
//
// The interpreter guest declares exactly ONE host import — nocturn.call — and the host side of it is
// a dispatcher over an agentkit.ToolSet: the guest calls nocturn.call(tool, args), the dispatcher
// looks the tool up and runs its Call. That toolset is the same gated set the model dispatches
// through, so a script reaches an action through the identical authorization path (the gate +
// out-of-band HITL). One gate is the reference monitor; adding a capability is a Go-side change and
// never rebuilds the interpreter.
//
// Pure compute needs no approval: a script that never calls the gate performs no action. Each action
// it does perform is gated individually by the tool it names.
package script

import (
	"context"
	_ "embed"
	"sync"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/sandbox"
)

// interpreterGuest is the QuickJS interpreter compiled to a WASI command (quickjs-ng core +
// qjs/nocturn-qjs.c), embedded so nocturn stays a single binary. It declares exactly one host import
// — nocturn.call — and exports malloc/free for the packed-ptr response ABI. Rebuild it only when the
// C shim changes (see qjs/build.sh).
//
//go:embed qjs/nocturn-qjs.wasm
var interpreterGuest []byte

// interpreterEngine is the process-wide QuickJS engine: the ~1.2 MB interpreter wasm compiled
// exactly ONCE and then shared, concurrently, by every code_run. Compilation is ~97% of a cold call,
// so sharing the compiled module is the whole performance win.
//
// sync.OnceValues compiles on first use (not at package init, so an unused binary pays nothing) and
// is safe for concurrent callers: the first triggers the compile while the rest block, and all
// receive the same engine. A compile failure is cached — the embedded wasm is deterministic, so a
// failure is a build bug, not a transient error worth retrying.
var interpreterEngine = sync.OnceValues(func() (*sandbox.Engine, error) {
	return sandbox.New(context.Background(), interpreterGuest, sandbox.EngineConfig{
		HostNames: []string{gateName},
	})
})

// New builds a Runner over the shared QuickJS interpreter engine and the given tools — the same set
// the model dispatches through, so script actions are gated and observed identically. This is the
// only constructor: a Runner always drives the interpreter (and always prepends the runtime
// prelude). A nil toolset yields an empty one (every call reports "unknown tool"). The engine is
// compiled lazily on the first Run, not here.
func New(tools agentkit.ToolSet) *Runner {
	if tools == nil {
		tools = agentkit.ToolSet{}
	}
	return &Runner{tools: tools}
}
