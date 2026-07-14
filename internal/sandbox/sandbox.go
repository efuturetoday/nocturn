// Package sandbox runs an untrusted WebAssembly guest (an interpreter such as
// QuickJS or MicroPython, or a compiled skill) under strict isolation on wazero.
//
// A guest starts from zero authority; the sandbox adds exactly what a real guest
// needs and nothing more: WASI stdio for input/output, an optional
// workspace directory confined by construction, brokered host-function imports,
// and hardening (a memory cap and a wall-clock deadline that traps runaway
// guests). The sandbox performs NO effect itself — every effect is a HostFunc
// supplied by the caller, which is where the broker/gateway sits.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"github.com/efuturetoday/nocturn/internal/deadline"
)

// hostModule is the single import module a guest sees; its members are the
// granted HostFuncs and nothing else ("unforgeable by absence").
const hostModule = "nocturn"

const (
	defaultTimeout  = 5 * time.Second
	defaultMaxPages = 1024 // 64 MiB (× 64 KiB pages)
)

// HostFunc is a brokered capability exposed to the guest. The guest calls it by
// Name over the (ptr,len) ABI; Fn receives the request bytes and returns the
// response bytes. Fn is where the broker/gateway lives — the sandbox never
// performs an effect itself.
type HostFunc struct {
	Name string
	Fn   func(ctx context.Context, req []byte) ([]byte, error)
}

// Config is a single run's grants and limits. The zero value grants nothing,
// preserving zero ambient authority.
type Config struct {
	Stdin     []byte        // fed to the guest as WASI fd 0
	Workspace string        // host dir mounted read/write at /work; "" = no filesystem
	Hosts     []HostFunc    // brokered host-function imports (module "nocturn")
	Timeout   time.Duration // wall-clock CPU bound (0 = default 5s)
	MaxPages  uint32        // memory cap in 64 KiB pages (0 = default 1024)
}

// Result is the guest's captured output.
type Result struct {
	Stdout []byte
	Stderr []byte
}

// Run instantiates guest under cfg and runs it to completion (its WASI command
// entry point), returning the captured output. A guest that traps, exits
// non-zero, exhausts memory, or exceeds the time limit returns an error
// alongside whatever output it produced.
func Run(ctx context.Context, guest []byte, cfg Config) (Result, error) {
	r := wazero.NewRuntimeWithConfig(ctx, hardened(cfg))
	defer r.Close(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	if err := registerHosts(ctx, r, cfg.Hosts); err != nil {
		return Result{}, err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// A pausable budget, not a plain timeout: while a host call is parked waiting
	// for an out-of-band human approval, hitl pauses this deadline so the wait
	// doesn't trap the (suspended) guest. It still bounds real execution time.
	runCtx, cancel := deadline.WithBudget(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	_, err := r.InstantiateWithConfig(runCtx, guest, moduleConfig(cfg, &stdout, &stderr))
	return finish(stdout.Bytes(), stderr.Bytes(), err, runCtx)
}

func hardened(cfg Config) wazero.RuntimeConfig {
	pages := cfg.MaxPages
	if pages == 0 {
		pages = defaultMaxPages
	}
	return wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true). // a cancelled/expired context traps the guest
		WithMemoryLimitPages(pages).
		WithMemoryCapacityFromMax(false)
}

func moduleConfig(cfg Config, stdout, stderr *bytes.Buffer) wazero.ModuleConfig {
	mc := wazero.NewModuleConfig().
		WithStdin(bytes.NewReader(cfg.Stdin)).
		WithStdout(stdout).
		WithStderr(stderr)
	if cfg.Workspace != "" {
		mc = mc.WithFSConfig(wazero.NewFSConfig().WithDirMount(cfg.Workspace, "/work"))
	}
	return mc
}

func registerHosts(ctx context.Context, r wazero.Runtime, hosts []HostFunc) error {
	if len(hosts) == 0 {
		return nil
	}
	b := r.NewHostModuleBuilder(hostModule)
	for _, h := range hosts {
		b = b.NewFunctionBuilder().WithFunc(hostFn(h)).Export(h.Name)
	}
	if _, err := b.Instantiate(ctx); err != nil {
		return fmt.Errorf("sandbox: host module: %w", err)
	}
	return nil
}

// hostFn adapts a HostFunc to the standard host↔wasm ABI used by QuickJS,
// Extism, wasm-bindgen and friends — the guest calls
//
//	nocturn.<name>(reqPtr, reqLen uint32) -> uint64   // packed (addr<<32 | size)
//
// The host reads the request, runs Fn, allocates the response INSIDE the guest
// via its exported malloc, writes it there, and returns a packed pointer the
// guest reads and then frees. A zero return means an empty response.
func hostFn(h HostFunc) func(context.Context, api.Module, uint32, uint32) uint64 {
	return func(ctx context.Context, mod api.Module, reqPtr, reqLen uint32) uint64 {
		view, ok := mod.Memory().Read(reqPtr, reqLen)
		if !ok {
			return 0
		}
		resp, err := h.Fn(ctx, append([]byte(nil), view...)) // copy the transient view out at once
		if err != nil {
			resp = []byte("error: " + err.Error())
		}
		return writeToGuest(ctx, mod, resp)
	}
}

// writeToGuest allocates len(b) bytes in the guest's linear memory via its
// exported malloc, writes b there, and returns the packed (addr<<32 | size).
// The guest reads size bytes at addr and then frees addr. Returns 0 for an
// empty response or if the guest has no usable allocator.
func writeToGuest(ctx context.Context, mod api.Module, b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	malloc := mod.ExportedFunction("malloc")
	if malloc == nil {
		return 0
	}
	res, err := malloc.Call(ctx, uint64(len(b)))
	if err != nil || len(res) == 0 {
		return 0
	}
	addr := uint32(res[0])
	if !mod.Memory().Write(addr, b) {
		return 0
	}
	return uint64(addr)<<32 | uint64(len(b))
}

func finish(stdout, stderr []byte, err error, runCtx context.Context) (Result, error) {
	res := Result{Stdout: stdout, Stderr: stderr}
	if err == nil {
		return res, nil
	}
	// A cancelled or expired context traps the guest and surfaces as a special
	// wazero ExitError (0xEFFFFFFF / 0xFFFFFFFF); report the context cause, not
	// that opaque trap code. The budget cancels via context.WithCancelCause, so
	// the real reason (DeadlineExceeded vs Canceled) is on the cause, not Err().
	if runCtx.Err() != nil {
		return res, fmt.Errorf("sandbox: run halted: %w", context.Cause(runCtx))
	}
	var exit *sys.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == 0:
		return res, nil // a WASI command's normal exit(0)
	case errors.As(err, &exit):
		return res, fmt.Errorf("sandbox: guest exited with code %d", exit.ExitCode())
	default:
		return res, fmt.Errorf("sandbox: %w", err)
	}
}
