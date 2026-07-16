package sandbox_test

import (
	"context"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/deadline"
	"github.com/efuturetoday/nocturn/internal/sandbox"
)

// The guest deadline is a pausable budget, and a HostFunc must be able to find it
// on its ctx — that is what lets hitl pause the deadline during an out-of-band
// approval. This pins the load-bearing assumption that wazero passes the run
// context (with its values) into host functions.
func TestRun_HostFuncCtxCarriesBudget(t *testing.T) {
	var sawPauser bool
	host := sandbox.HostFunc{Name: "echo", Fn: func(ctx context.Context, req []byte) ([]byte, error) {
		sawPauser = deadline.PauserFrom(ctx) != nil
		return req, nil
	}}
	if _, err := sandbox.Run(context.Background(), echoGuest, sandbox.Config{
		Stdin: []byte("x"),
		Hosts: []sandbox.HostFunc{host},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sawPauser {
		t.Fatal("HostFunc ctx did not carry the sandbox budget (PauserFrom == nil)")
	}
}

//go:embed testdata/echo.wasm
var echoGuest []byte

//go:embed testdata/loop.wasm
var loopGuest []byte

//go:embed testdata/fsprobe.wasm
var fsGuest []byte

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

// A HostFunc that returns an error does not crash the host: the ABI surfaces it to
// the guest as an "error: <msg>" response string (which an interpreter's binding
// turns into a language-level exception). Here the echo guest writes that response
// straight to stdout, so we can assert the prefix + message round-trip.
func TestRun_HostCallError_SurfacesAsErrorString(t *testing.T) {
	host := sandbox.HostFunc{Name: "echo", Fn: func(context.Context, []byte) ([]byte, error) {
		return nil, errors.New("kaboom")
	}}
	res, err := sandbox.Run(context.Background(), echoGuest, sandbox.Config{
		Stdin: []byte("ignored"),
		Hosts: []sandbox.HostFunc{host},
	})
	if err != nil {
		t.Fatalf("run should not fail on a guest-visible host error: %v (stderr=%s)", err, res.Stderr)
	}
	if got := string(res.Stdout); got != "error: kaboom" {
		t.Fatalf("stdout = %q, want %q", got, "error: kaboom")
	}
}

// A runaway guest is trapped by the wall-clock deadline.
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

// The workspace is the ONLY filesystem: the guest can create a file inside /work
// but cannot escape it to reach the host FS. Confinement is allowlist-by-mount.
func TestRun_Workspace_ConfinedReadWrite(t *testing.T) {
	ws := t.TempDir()
	res, err := sandbox.Run(context.Background(), fsGuest, sandbox.Config{Workspace: ws})
	if err != nil {
		t.Fatalf("run: %v (stderr=%s)", err, res.Stderr)
	}
	// result byte: bit0 = create-in-/work ok, bit1 = escape open ok (must be 0)
	if len(res.Stdout) != 1 || res.Stdout[0] != 0x01 {
		t.Fatalf("result = %v, want [1] (write ok, escape blocked)", res.Stdout)
	}
	// the create really landed inside the host workspace dir
	if _, err := os.Stat(filepath.Join(ws, "probe.txt")); err != nil {
		t.Fatalf("workspace file not created on host: %v", err)
	}
}
