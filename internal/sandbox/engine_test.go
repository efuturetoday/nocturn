package sandbox_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/sandbox"
)

// CompileModule does not resolve imports, so an engine built for a guest that
// imports a name it never registered still constructs — but the first Run cannot
// instantiate it. echoGuest imports nocturn.echo; an engine with no HostNames
// gives it no such import. This is the zero-authority instantiation floor.
func TestNew_UnknownImport_InstantiateFails(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{})
	if err != nil {
		t.Fatalf("New should succeed (imports are not resolved at compile time): %v", err)
	}
	defer eng.Close(context.Background())

	if _, err := eng.Run(context.Background(), sandbox.Config{Stdin: []byte("x")}); err == nil {
		t.Fatal("guest instantiated with an unregistered import — isolation is broken")
	}
}

// MaxPages == 0 must fall back to the default, not to zero pages. The echo guest
// declares a 4-page minimum memory; if the engine passed 0 through to wazero's
// memory limit the guest could not instantiate. That it runs proves defaulting.
func TestNew_ZeroMaxPages_UsesDefault(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{
		HostNames: []string{"echo"},
		MaxPages:  0,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close(context.Background())

	res, err := eng.Run(context.Background(), sandbox.Config{
		Stdin: []byte("ping"),
		Hosts: []sandbox.HostFunc{echoHost()},
	})
	if err != nil {
		t.Fatalf("Run under defaulted MaxPages: %v (stderr=%s)", err, res.Stderr)
	}
	if got := string(res.Stdout); got != "ping" {
		t.Fatalf("stdout = %q, want %q", got, "ping")
	}
}

// A dispatcher whose name has no matching trampoline on the engine can never be
// reached from the guest, so Run rejects it loudly (a wiring bug) rather than
// wiring a dead grant — and never silently drops it.
func TestRun_HostNotRegistered_FailsLoud(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{
		HostNames: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close(context.Background())

	stray := sandbox.HostFunc{Name: "call", Fn: func(context.Context, []byte) ([]byte, error) { return nil, nil }}
	_, err = eng.Run(context.Background(), sandbox.Config{Hosts: []sandbox.HostFunc{stray}})
	if err == nil {
		t.Fatal("Run accepted a dispatcher with no matching trampoline")
	}
	if want := `host "call" not registered`; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want it to contain %q", err, want)
	}
}

// The import is registered (so the guest instantiates) but this call grants no
// dispatcher for it. The trampoline must fail closed with a guest-visible error
// string — never a silent allow, never a nil-map panic. The echo guest writes
// that response straight to stdout, so we can assert it round-trips.
func TestTrampoline_UngrantedDispatcher_FailsClosed(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{
		HostNames: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close(context.Background())

	res, err := eng.Run(context.Background(), sandbox.Config{Stdin: []byte("x")}) // no Hosts
	if err != nil {
		t.Fatalf("Run should not fail on a fail-closed host call: %v (stderr=%s)", err, res.Stderr)
	}
	if want := `error: host function "echo" not granted`; string(res.Stdout) != want {
		t.Fatalf("stdout = %q, want %q", res.Stdout, want)
	}
}

// The wazero Memory.Read pitfall: it returns a VIEW into live guest memory, not a
// copy, so the trampoline must copy the request bytes out (append([]byte(nil),
// view...)) before the guest can mutate that region. The mutate guest hands its
// input to the host and then overwrites the same region with 0xFF; the bytes the
// host retained must still equal the input. If the trampoline leaked the raw view,
// the retained bytes would read back as 0xFF.
func TestTrampoline_CopiesMemoryViewBeforeReturn(t *testing.T) {
	const input = "abcdefgh"
	var retained []byte
	host := sandbox.HostFunc{Name: "echo", Fn: func(_ context.Context, req []byte) ([]byte, error) {
		retained = req // keep the slice the trampoline handed us, across the call
		return []byte("ok"), nil
	}}
	eng, err := sandbox.New(context.Background(), mutateGuest, sandbox.EngineConfig{
		HostNames: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close(context.Background())

	if _, err := eng.Run(context.Background(), sandbox.Config{
		Stdin: []byte(input),
		Hosts: []sandbox.HostFunc{host},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(retained) != input {
		t.Fatalf("retained request = %q, want %q — the trampoline leaked a live memory view", retained, input)
	}
}

// A small MaxPages cap is enforced: the grow guest asks memory.grow for 100 pages
// and then touches the page it wanted. Under the cap the grow is refused and the
// touch traps out-of-bounds — the host never actually allocates the memory. The
// trap surfaces as an error, and it is NOT a deadline (the guest is fast).
func TestRun_MemoryCapEnforced(t *testing.T) {
	eng, err := sandbox.New(context.Background(), growGuest, sandbox.EngineConfig{MaxPages: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close(context.Background())

	_, err = eng.Run(context.Background(), sandbox.Config{})
	if err == nil {
		t.Fatal("guest grew past the memory cap without trapping — the cap is not enforced")
	}
	if errors.Is(err, agentkit.ErrTurnTimeout) {
		t.Fatalf("err = %v, want a memory trap, not a deadline", err)
	}
}

// The load-bearing property of the compile-once design: one Engine and one
// compiled host module serve every call, but each call's dispatcher rides its own
// ctx, so authority never crosses between concurrent runs. N goroutines each run
// the echo guest with a dispatcher returning a token unique to that call; every
// guest must see back exactly its own token. Run under -race.
func TestRun_ConcurrentRuns_NoAuthorityCrossing(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{
		HostNames: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close(context.Background())

	const n = 32
	got := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
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

	for i := range n {
		if want := fmt.Sprintf("token-%d", i); got[i] != want {
			t.Errorf("run %d saw %q, want %q — a dispatcher crossed between calls", i, got[i], want)
		}
	}
}

// After Close the runtime is gone: a later Run must fail rather than run on a
// half-torn-down engine.
func TestEngine_Reuse_AfterClose_Fails(t *testing.T) {
	eng, err := sandbox.New(context.Background(), echoGuest, sandbox.EngineConfig{
		HostNames: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := eng.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := eng.Run(context.Background(), sandbox.Config{
		Stdin: []byte("x"),
		Hosts: []sandbox.HostFunc{echoHost()},
	}); err == nil {
		t.Fatal("Run succeeded on a closed engine")
	}
}

// A runaway guest is trapped by the pausable wall-clock budget, and the error
// reports the CAUSE (agentkit.ErrTurnTimeout) rather than the opaque wazero exit
// code the trap surfaces as. Real time, small timeout: synctest cannot drive this
// case — an infinite wasm loop is CPU-bound and never blocks, so synthetic time
// would never advance (see the [synctest] budget tests below, which block in a
// host call so time can move).
func TestRun_DeadlineExceeded_TrapsAndReportsCause(t *testing.T) {
	eng, err := sandbox.New(context.Background(), loopGuest, sandbox.EngineConfig{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close(context.Background())

	_, err = eng.Run(context.Background(), sandbox.Config{Timeout: 200 * time.Millisecond})
	if !errors.Is(err, agentkit.ErrTurnTimeout) {
		t.Fatalf("err = %v, want it to wrap agentkit.ErrTurnTimeout", err)
	}
}

// Timeout == 0 falls back to the default 5s budget. Driven under synctest with a
// host function that blocks on ctx.Done(): the guest parks in the host call so all
// goroutines are blocked and synthetic time advances to exactly the default before
// the budget fires. This pins the default without waiting five real seconds.
func TestRun_ZeroTimeout_UsesDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var firedAt time.Duration
		host := sandbox.HostFunc{Name: "echo", Fn: func(ctx context.Context, req []byte) ([]byte, error) {
			<-ctx.Done() // park until the budget fires
			firedAt = time.Since(start)
			return req, nil
		}}
		_, err := sandbox.Run(context.Background(), echoGuest, sandbox.Config{
			Stdin: []byte("x"),
			Hosts: []sandbox.HostFunc{host}, // Timeout omitted → default
		})
		if firedAt != 5*time.Second {
			t.Fatalf("budget fired at %v, want the 5s default", firedAt)
		}
		if !errors.Is(err, agentkit.ErrTurnTimeout) {
			t.Fatalf("err = %v, want agentkit.ErrTurnTimeout", err)
		}
	})
}

// A cancelled parent context halts the guest and is reported as Canceled — NOT as
// a deadline. finish reads context.Cause, and a plain WithCancel propagates
// context.Canceled through the pausable budget's derived context.
func TestRun_ContextCanceled_ReportsCanceled(t *testing.T) {
	eng, err := sandbox.New(context.Background(), loopGuest, sandbox.EngineConfig{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := eng.Run(ctx, sandbox.Config{Timeout: 10 * time.Second})
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the guest enter its loop, then cancel
	cancel()

	err = <-errc
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, agentkit.ErrTurnTimeout) {
		t.Fatalf("err = %v, want Canceled, not a deadline", err)
	}
}

// The guest budget is PAUSABLE: while a host call parks on an out-of-band approval
// (modelled by agentkit.Pause + a long wait), the budget must not burn. Driven
// under synctest: with the budget paused, a 10s host wait under a 1s budget still
// completes cleanly; without the pause, the same wait trips the 1s budget. This is
// what keeps a nested approval from ever consuming the guest's real-execution cap.
func TestPausableBudget_NotBurnedDuringHostPause(t *testing.T) {
	tests := []struct {
		name    string
		pause   bool
		wantErr bool
	}{
		{name: "paused during host wait: budget survives", pause: true, wantErr: false},
		{name: "not paused: host wait trips the budget", pause: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				host := sandbox.HostFunc{Name: "echo", Fn: func(ctx context.Context, req []byte) ([]byte, error) {
					if tt.pause {
						resume := agentkit.Pause(ctx)
						defer resume()
					}
					time.Sleep(10 * time.Second) // out-of-band wait, far past the 1s budget
					return req, nil
				}}
				res, err := sandbox.Run(context.Background(), echoGuest, sandbox.Config{
					Stdin:   []byte("hello"),
					Hosts:   []sandbox.HostFunc{host},
					Timeout: 1 * time.Second,
				})
				switch {
				case tt.wantErr && !errors.Is(err, agentkit.ErrTurnTimeout):
					t.Fatalf("err = %v, want the budget to fire (agentkit.ErrTurnTimeout)", err)
				case !tt.wantErr && err != nil:
					t.Fatalf("Run: %v — a paused budget must not burn during the host wait", err)
				case !tt.wantErr && string(res.Stdout) != "hello":
					t.Fatalf("stdout = %q, want %q", res.Stdout, "hello")
				}
			})
		})
	}
}
