package sandbox_test

import (
	"context"
	_ "embed"
	"errors"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/sandbox"
)

//go:embed testdata/echo.wasm
var echoGuest []byte

//go:embed testdata/loop.wasm
var loopGuest []byte

// echoHost returns the request unchanged — enough to prove the round-trip ABI.
func echoHost() sandbox.HostFunc {
	return sandbox.HostFunc{Name: "echo", Fn: func(_ context.Context, req []byte) ([]byte, error) {
		return req, nil
	}}
}

// The standard host↔wasm ABI end to end: stdin reaches the guest, the guest
// calls the host function (which allocates the response inside the guest via its
// exported malloc and returns a packed pointer), and the result comes back on
// stdout.
func TestRun_HostCallABI_ViaStdio(t *testing.T) {
	res, err := sandbox.Run(context.Background(), echoGuest, sandbox.Config{
		Stdin: []byte("hello sandbox"),
		Hosts: []sandbox.HostFunc{echoHost()},
	})
	if err != nil {
		t.Fatalf("run: %v (stderr=%s)", err, res.Stderr)
	}
	if got := string(res.Stdout); got != "hello sandbox" {
		t.Fatalf("stdout = %q, want %q", got, "hello sandbox")
	}
}

// A runaway guest is trapped by the wall-clock deadline (the #422 guarantee).
func TestRun_DeadlineTrapsRunawayGuest(t *testing.T) {
	_, err := sandbox.Run(context.Background(), loopGuest, sandbox.Config{
		Timeout: 200 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

// Unforgeable by absence — the zero-authority floor: a guest that imports a host
// function it was not granted cannot even instantiate. echoGuest imports
// nocturn.echo; run with no Hosts, that import does not exist in its world.
func TestRun_UngrantedImport_CannotInstantiate(t *testing.T) {
	if _, err := sandbox.Run(context.Background(), echoGuest, sandbox.Config{}); err == nil {
		t.Fatal("guest reached an ungranted host function — isolation is broken")
	}
}
