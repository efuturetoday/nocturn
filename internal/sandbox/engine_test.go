package sandbox_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/sandbox"
)

// The load-bearing security property of the compile-once design: one Engine and
// one compiled host module serve every call, but each call's dispatcher rides its
// own ctx, so authority never crosses between concurrent runs. N goroutines each
// run the echo guest with a dispatcher that returns a token unique to that call;
// every guest must see back exactly its own token. Run under -race.
func TestEngine_ConcurrentDispatchersNeverCross(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{
		HostNames: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}
	defer eng.Close(context.Background())

	const n = 32
	got := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := fmt.Sprintf("token-%d", i)
			host := sandbox.HostFunc{Name: "echo", Fn: func(context.Context, []byte) ([]byte, error) {
				return []byte(token), nil // this call's dispatcher, unique per i
			}}
			res, err := eng.Run(context.Background(), sandbox.Config{
				Stdin: []byte("ping"),
				Hosts: []sandbox.HostFunc{host},
			})
			if err != nil {
				t.Errorf("run %d: %v (stderr=%s)", i, err, res.Stderr)
				return
			}
			got[i] = string(res.Stdout)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if want := fmt.Sprintf("token-%d", i); got[i] != want {
			t.Errorf("run %d saw %q, want %q — a dispatcher crossed between calls", i, got[i], want)
		}
	}
}

// Zero-authority, layer (a): a guest that imports a name the engine did not
// register cannot instantiate. echoGuest imports nocturn.echo; an engine that
// only exports nocturn.call gives it no such import.
func TestEngine_UnregisteredImport_CannotInstantiate(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{
		HostNames: []string{"call"},
	})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}
	defer eng.Close(context.Background())

	if _, err := eng.Run(context.Background(), sandbox.Config{Stdin: []byte("x")}); err == nil {
		t.Fatal("guest instantiated with an unregistered import — isolation is broken")
	}
}

// Zero-authority, layer (b): the import is registered (so the guest instantiates)
// but this call grants no dispatcher for it. The trampoline must fail closed with
// a guest-visible error string, never a silent allow. The echo guest writes that
// response straight to stdout, so we can assert it round-trips.
func TestEngine_NoDispatcher_FailClosed(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{
		HostNames: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}
	defer eng.Close(context.Background())

	res, err := eng.Run(context.Background(), sandbox.Config{Stdin: []byte("x")}) // no Hosts
	if err != nil {
		t.Fatalf("run should not fail on a fail-closed host call: %v (stderr=%s)", err, res.Stderr)
	}
	if want := `error: host function "echo" not granted`; string(res.Stdout) != want {
		t.Fatalf("stdout = %q, want %q", res.Stdout, want)
	}
}

// A dispatcher whose name has no matching trampoline on the engine can never be
// reached from the guest, so Run rejects it loudly rather than wiring a dead grant.
func TestEngine_Run_RejectsUnregisteredHost(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{
		HostNames: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}
	defer eng.Close(context.Background())

	stray := sandbox.HostFunc{Name: "call", Fn: func(context.Context, []byte) ([]byte, error) { return nil, nil }}
	if _, err := eng.Run(context.Background(), sandbox.Config{Hosts: []sandbox.HostFunc{stray}}); err == nil {
		t.Fatal("Run accepted a dispatcher with no matching trampoline")
	}
}

// A runaway guest is trapped by the wall-clock deadline, and — crucially for a
// reused Engine — the trap does not poison the runtime: a later run on the same
// engine still executes and is trapped just as cleanly.
func TestEngine_DeadlineTrapsRunawayGuest(t *testing.T) {
	eng, err := sandbox.New(context.Background(), loopGuest, sandbox.EngineConfig{})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}
	defer eng.Close(context.Background())

	for i := 0; i < 2; i++ {
		_, err := eng.Run(context.Background(), sandbox.Config{Timeout: 150 * time.Millisecond})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("run %d: err = %v, want deadline exceeded", i, err)
		}
	}
}

// A long-lived Engine reused across many runs must release each instance's
// resources (mod.Close per run) — no error accumulation, no degradation. This
// exercises the release path M times; RSS stability is watched manually.
func TestEngine_ReusedAcrossManyRuns(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{
		HostNames: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("New engine: %v", err)
	}
	defer eng.Close(context.Background())

	echo := sandbox.HostFunc{Name: "echo", Fn: func(_ context.Context, req []byte) ([]byte, error) {
		return req, nil
	}}
	const m = 200
	for i := 0; i < m; i++ {
		res, err := eng.Run(context.Background(), sandbox.Config{
			Stdin: []byte("hello"),
			Hosts: []sandbox.HostFunc{echo},
		})
		if err != nil {
			t.Fatalf("run %d: %v (stderr=%s)", i, err, res.Stderr)
		}
		if string(res.Stdout) != "hello" {
			t.Fatalf("run %d: stdout = %q, want %q", i, res.Stdout, "hello")
		}
	}
}
