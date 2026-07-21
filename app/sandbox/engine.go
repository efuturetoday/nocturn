package sandbox

import (
	"bytes"
	"context"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/efuturetoday/nocturn/app/deadline"
)

// EngineConfig fixes the compile-scoped knobs of an Engine. wazero bakes the
// memory limit and the context-done trap into a module at CompileModule time,
// so they belong to the Engine, not to an individual Run.
type EngineConfig struct {
	// HostNames are the exported functions of the single import module "nocturn"
	// the guest may see. Empty means no host module at all (pure zero authority).
	HostNames []string
	// MaxPages caps guest memory in 64 KiB pages; 0 = default 1024 (64 MiB).
	MaxPages uint32
}

// Engine is a guest compiled once and reused across many Run calls, concurrently.
// Compilation (~97% of a cold call for a large interpreter guest) happens in
// New; each Run only instantiates the already-compiled module, which is the
// ~130× win over recompiling per call.
type Engine struct {
	rt        wazero.Runtime
	code      wazero.CompiledModule
	hostNames map[string]struct{} // registered imports; used for Run's fail-loud check
}

// New builds the runtime, registers the host module (one stateless
// trampoline per HostNames entry), and compiles guest — all once. The per-call
// dispatchers ride the ctx at Run time (see withHosts/trampoline), so the single
// registered module is fixed and shareable across concurrent instantiations.
//
// CompileModule does not resolve imports, so a guest that imports a name not in
// HostNames compiles here but fails at InstantiateModule — preserving the
// zero-authority instantiation floor.
func New(ctx context.Context, guest []byte, cfg EngineConfig) (*Engine, error) {
	pages := cfg.MaxPages
	if pages == 0 {
		pages = defaultMaxPages
	}
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true). // a cancelled/expired context traps the guest
		WithMemoryLimitPages(pages).
		WithMemoryCapacityFromMax(false))

	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	names := make(map[string]struct{}, len(cfg.HostNames))
	if len(cfg.HostNames) > 0 {
		b := rt.NewHostModuleBuilder(hostModule)
		for _, name := range cfg.HostNames {
			b = b.NewFunctionBuilder().WithFunc(trampoline(name)).Export(name)
			names[name] = struct{}{}
		}
		if _, err := b.Instantiate(ctx); err != nil {
			_ = rt.Close(ctx)
			return nil, fmt.Errorf("sandbox: host module: %w", err)
		}
	}

	code, err := rt.CompileModule(ctx, guest)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("sandbox: compile guest: %w", err)
	}
	return &Engine{rt: rt, code: code, hostNames: names}, nil
}

// Run instantiates the compiled guest under cfg and runs it to completion,
// returning the captured output. Safe to call concurrently: each call gets a
// fresh module instance (its own memory, globals, and interpreter heap) and its
// own dispatcher map on ctx, so no state and no authority crosses between calls.
func (e *Engine) Run(ctx context.Context, cfg Config) (Result, error) {
	for _, h := range cfg.Hosts {
		if _, ok := e.hostNames[h.Name]; !ok {
			// Fail loud: a dispatcher with no matching trampoline can never be
			// reached from the guest, so silently accepting it would hide a
			// wiring bug (and never drop a limit silently — same principle).
			return Result{}, fmt.Errorf("sandbox: host %q not registered on this engine", h.Name)
		}
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// A pausable budget, not a plain timeout: while a host call is parked waiting
	// for an out-of-band human approval, hitl pauses this deadline so the wait
	// doesn't trap the (suspended) guest. It still bounds real execution time.
	budgetCtx, cancel := deadline.WithBudget(ctx, timeout)
	defer cancel()
	// Stamp this call's dispatchers onto the same ctx wazero hands the trampoline
	// during _start. Unexported seam, stamped in exactly one place, so no caller
	// can forget it and nothing outside the package can forge it.
	runCtx := withHosts(budgetCtx, cfg.Hosts)

	var stdout, stderr bytes.Buffer
	mod, err := e.rt.InstantiateModule(runCtx, e.code, moduleConfig(cfg, &stdout, &stderr).WithName(""))
	if mod != nil {
		_ = mod.Close(ctx) // release this instance's resources on the long-lived runtime
	}
	return finish(runCtx, stdout.Bytes(), stderr.Bytes(), err)
}

// Close releases the compiled module and the runtime. After Close the Engine
// must not be used. Per-call instances are already closed by each Run.
func (e *Engine) Close(ctx context.Context) error {
	_ = e.code.Close(ctx)
	return e.rt.Close(ctx)
}

// hostsKey addresses the per-call dispatcher map on the run context. It is an
// unexported struct type, so nothing outside this package can read or forge it.
type hostsKey struct{}

type dispatchFn func(context.Context, []byte) ([]byte, error)

// withHosts stamps a call's dispatchers onto ctx, keyed by HostFunc name. The
// map is written once here and only read afterwards, so it is safe to share
// across any goroutines wazero uses within a single instantiation.
func withHosts(ctx context.Context, hosts []HostFunc) context.Context {
	m := make(map[string]dispatchFn, len(hosts))
	for _, h := range hosts {
		m[h.Name] = h.Fn
	}
	return context.WithValue(ctx, hostsKey{}, m)
}

// trampoline is the fixed host function registered under name at New time.
// It holds no per-call state: it reads the real dispatcher for its name off the
// run context, so one compiled host module serves every call, concurrently.
//
// The (reqPtr,reqLen) -> packed(addr<<32|size) ABI, the out-copy of the transient
// memory view, the "error: " response prefix, and writeToGuest (guest malloc) are
// the standard host↔wasm contract used by QuickJS, Extism and friends. The host
// reads the request, runs the dispatcher, allocates the response INSIDE the guest
// via its exported malloc, writes it there, and returns a packed pointer the guest
// reads and then frees. A zero return means an empty response.
func trampoline(name string) func(context.Context, api.Module, uint32, uint32) uint64 {
	return func(ctx context.Context, mod api.Module, reqPtr, reqLen uint32) uint64 {
		view, ok := mod.Memory().Read(reqPtr, reqLen)
		if !ok {
			return 0
		}
		m, _ := ctx.Value(hostsKey{}).(map[string]dispatchFn)
		fn, ok := m[name]
		if !ok {
			// Fail closed: the import exists (it was registered), but this call
			// granted no dispatcher for it. Deny as a guest-visible error string,
			// never a silent allow and never a nil-map panic (nil-map read is safe).
			return writeToGuest(ctx, mod, []byte(`error: host function "`+name+`" not granted`))
		}
		resp, err := fn(ctx, append([]byte(nil), view...)) // copy the transient view out at once
		if err != nil {
			resp = []byte("error: " + err.Error())
		}
		return writeToGuest(ctx, mod, resp)
	}
}

// hostNamesOf extracts the HostFunc names from cfg for the one-shot Run wrapper,
// whose throwaway Engine registers exactly the imports cfg.Hosts provides.
func hostNamesOf(hosts []HostFunc) []string {
	if len(hosts) == 0 {
		return nil
	}
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Name
	}
	return names
}
