package script_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"github.com/efuturetoday/nocturn/internal/sandbox"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// benchNoopCall satisfies the one host import; a trivial script never calls it,
// but the import must resolve for the guest to instantiate.
func benchNoopCall(_ context.Context, _ api.Module, _, _ uint32) uint64 { return 0 }

func benchInstantiate(b *testing.B, rt wazero.Runtime, cm wazero.CompiledModule, src []byte) {
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		var out bytes.Buffer
		mod, err := rt.InstantiateModule(ctx, cm, wazero.NewModuleConfig().
			WithName("").WithStdin(bytes.NewReader(src)).WithStdout(&out))
		var exit *sys.ExitError
		if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 0) {
			b.Fatalf("instantiate: %v", err)
		}
		if mod != nil {
			_ = mod.Close(ctx)
		}
	}
}

func benchRuntime(b *testing.B) (wazero.Runtime, wazero.CompiledModule) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(1024))
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	if _, err := rt.NewHostModuleBuilder("nocturn").
		NewFunctionBuilder().WithFunc(benchNoopCall).Export("call").Instantiate(ctx); err != nil {
		b.Fatal(err)
	}
	cm, err := rt.CompileModule(ctx, script.InterpreterGuest())
	if err != nil {
		b.Fatal(err)
	}
	return rt, cm
}

// The production path after compile-once: script.New().Run() over the shared
// interpreter engine. The engine is warmed before the timer so the one-time
// compile isn't billed to the loop — this measures instantiate + QuickJS boot +
// prelude eval + trivial eval + close, the true per-call cost (~2.5 ms).
func BenchmarkInterpreter_Run_Engine(b *testing.B) {
	r := script.New(tool.NewRegistry())
	ctx := context.Background()
	if _, err := r.Run(ctx, "1+1"); err != nil { // warm the shared engine (one-time compile)
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Run(ctx, "1+1"); err != nil {
			b.Fatal(err)
		}
	}
}

// The old per-call cost, preserved as the before-baseline: the one-shot sandbox.Run
// recompiles the ~1.2 MB wasm every call (throwaway engine). The delta vs Run_Engine
// is the compile cost that compile-once eliminates (~328 ms → ~2.5 ms).
func BenchmarkInterpreter_Run_Recompile(b *testing.B) {
	ctx := context.Background()
	gate := sandbox.HostFunc{Name: "call", Fn: func(context.Context, []byte) ([]byte, error) { return nil, nil }}
	src := []byte(script.Prelude() + "\n1+1")
	guest := script.InterpreterGuest()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := sandbox.Run(ctx, guest, sandbox.Config{
			Stdin: src,
			Hosts: []sandbox.HostFunc{gate},
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// Pure compile cost (what compile-once would eliminate): CompileModule only.
func BenchmarkInterpreter_CompileOnly(b *testing.B) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig())
	defer rt.Close(ctx)
	guest := script.InterpreterGuest()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm, err := rt.CompileModule(ctx, guest)
		if err != nil {
			b.Fatal(err)
		}
		_ = cm.Close(ctx)
	}
}

// Compile-once: instantiate + QuickJS boot + prelude eval + trivial eval, NO
// recompile. The delta vs Run_Current is the compile cost.
func BenchmarkInterpreter_CompileOnce_WithPrelude(b *testing.B) {
	rt, cm := benchRuntime(b)
	defer rt.Close(context.Background())
	src := []byte(script.Prelude() + "\n1+1")
	b.ResetTimer()
	benchInstantiate(b, rt, cm, src)
}

// Compile-once WITHOUT the prelude: the delta vs WithPrelude is the prelude
// parse+eval cost per call (what a wizer snapshot would eliminate).
func BenchmarkInterpreter_CompileOnce_NoPrelude(b *testing.B) {
	rt, cm := benchRuntime(b)
	defer rt.Close(context.Background())
	src := []byte("1+1")
	b.ResetTimer()
	benchInstantiate(b, rt, cm, src)
}
