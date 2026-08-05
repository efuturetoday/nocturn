// Package sandbox runs an untrusted WebAssembly guest (an interpreter such as
// QuickJS or MicroPython, or a compiled skill) under strict isolation on wazero.
//
// A guest starts from zero authority; the sandbox adds exactly what a real guest
// needs and nothing more: WASI stdio for input/output, the clock and entropy an
// interpreter cannot work correctly without, an optional workspace directory
// confined by construction, brokered host-function imports, and hardening (a
// memory cap and a wall-clock deadline that traps runaway guests). Reading the
// time is not authority; see moduleConfig for why the deterministic defaults
// are the more dangerous choice. The sandbox performs NO action itself — every action is a HostFunc
// supplied by the caller, which is where the broker/gateway sits.
//
// Guests are compiled once into an Engine (compilation dominates per-call cost)
// and instantiated per Run, concurrently. Run is a one-shot convenience over a
// throwaway Engine for callers that run a guest a single time.
package sandbox

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/sys"
)

// hostModule is the single import module a guest sees; its members are the
// granted HostFuncs and nothing else ("unforgeable by absence").
const hostModule = "nocturn"

const (
	defaultTimeout  = 5 * time.Second
	defaultMaxPages = 1024 // 64 MiB (× 64 KiB pages)
)

// HostFunc is a brokered host function exposed to the guest. The guest calls it by
// Name over the (ptr,len) ABI; Fn receives the request bytes and returns the
// response bytes. Fn is where the broker/gateway lives — the sandbox never
// performs an action itself.
type HostFunc struct {
	Name string
	Fn   func(ctx context.Context, req []byte) ([]byte, error)
}

// Config is a single run's grants and limits. The zero value grants nothing,
// preserving zero ambient authority. The memory cap is not here: it is
// compile-scoped and therefore an EngineConfig knob (see engine.go).
type Config struct {
	Stdin     []byte        // fed to the guest as WASI fd 0
	Workspace string        // host dir mounted read/write at /work; "" = no filesystem
	Hosts     []HostFunc    // brokered host-function imports (module "nocturn")
	Timeout   time.Duration // wall-clock deadline (0 = default 5s)
}

// Result is the guest's captured output.
type Result struct {
	Stdout []byte
	Stderr []byte
}

// Run compiles guest, instantiates it under cfg, and runs it to completion (its
// WASI command entry point), returning the captured output. It is a one-shot
// convenience over an Engine: it compiles, runs once, and closes everything.
// Callers that run the same guest repeatedly should build an Engine once and
// reuse it — compilation is ~97% of a cold call for a large interpreter guest.
//
// A guest that traps, exits non-zero, exhausts memory, or exceeds the time limit
// returns an error alongside whatever output it produced.
func Run(ctx context.Context, guest []byte, cfg Config) (Result, error) {
	eng, err := New(ctx, guest, EngineConfig{HostNames: hostNamesOf(cfg.Hosts)})
	if err != nil {
		return Result{}, err
	}
	defer eng.Close(ctx)
	return eng.Run(ctx, cfg)
}

// moduleConfig assembles what a guest may see of the outside world: its stdio, an optional confined
// directory, and — deliberately — the real clock and real entropy.
//
// wazero's defaults for those last two are deterministic stand-ins: a clock frozen at 2022-01-01 and
// a fixed pseudo-random stream. That reads like hardening and is not. It costs correctness outright,
// since a guest formatting a date silently produces a wrong one; and it costs security, because a
// frozen clock is what an interpreter seeds its PRNG from. Measured: with the default clock,
// QuickJS's Math.random() returns the identical value on every run of every script, forever — which
// the prelude was using to pick multipart boundaries, so a crafted field value could close a part
// early and forge the rest of the body.
//
// A real clock does not widen what a guest can DO. It has no sockets, no threads, and no second
// party to time against; a timing side channel needs a target, and inside this instance there is
// none. Authority still comes only from the brokered host functions.
//
// Entropy comes from crypto/rand and reaches the guest as WASI random_get. Nothing here relies on
// the interpreter's own PRNG for anything that must be unguessable — the QuickJS shim exposes
// crypto.getRandomValues over this same source for that.
func moduleConfig(cfg Config, stdout, stderr *bytes.Buffer) wazero.ModuleConfig {
	mc := wazero.NewModuleConfig().
		WithStdin(bytes.NewReader(cfg.Stdin)).
		WithStdout(stdout).
		WithStderr(stderr).
		WithRandSource(cryptorand.Reader).
		WithSysWalltime().
		WithSysNanotime()
	if cfg.Workspace != "" {
		mc = mc.WithFSConfig(wazero.NewFSConfig().WithDirMount(cfg.Workspace, "/work"))
	}
	return mc
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

func finish(runCtx context.Context, stdout, stderr []byte, err error) (Result, error) {
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
