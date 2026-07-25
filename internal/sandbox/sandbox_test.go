package sandbox_test

import (
	"context"
	_ "embed"
	"testing"

	"github.com/efuturetoday/nocturn/internal/sandbox"
)

// The guest corpus. echo/loop/fsprobe mirror internal/sandbox; mutate/grow/stdio
// are authored for this package (see the matching .wat files under testdata).
var (
	//go:embed testdata/echo.wasm
	echoGuest []byte
	//go:embed testdata/loop.wasm
	loopGuest []byte
	//go:embed testdata/fsprobe.wasm
	fsGuest []byte
	//go:embed testdata/mutate.wasm
	mutateGuest []byte
	//go:embed testdata/grow.wasm
	growGuest []byte
	//go:embed testdata/stdio.wasm
	stdioGuest []byte
)

// echoHost returns the request unchanged — enough to prove the round-trip ABI.
func echoHost() sandbox.HostFunc {
	return sandbox.HostFunc{Name: "echo", Fn: func(_ context.Context, req []byte) ([]byte, error) {
		return req, nil
	}}
}

// The standard host↔wasm ABI end to end over the one-shot Run: stdin reaches the
// guest, the guest calls the host function (which allocates its response inside
// the guest via the exported malloc and returns a packed pointer), and the result
// comes back on stdout. This anchors every later test that reuses the echo guest.
func TestRun_HostCallABI_ViaStdio(t *testing.T) {
	res, err := sandbox.Run(context.Background(), echoGuest, sandbox.Config{
		Stdin: []byte("hello sandbox"),
		Hosts: []sandbox.HostFunc{echoHost()},
	})
	if err != nil {
		t.Fatalf("Run: %v (stderr=%s)", err, res.Stderr)
	}
	if got := string(res.Stdout); got != "hello sandbox" {
		t.Fatalf("stdout = %q, want %q", got, "hello sandbox")
	}
}

// Zero-authority floor, no host module reachable: the echo guest imports
// nocturn.echo, but a Run with no Hosts registers no host module at all, so the
// import cannot be linked and the guest never instantiates. "Unforgeable by
// absence" — the guest cannot reach a capability that was not handed to it.
func TestRun_NoNetGrant_NoHostFunc(t *testing.T) {
	if _, err := sandbox.Run(context.Background(), echoGuest, sandbox.Config{Stdin: []byte("x")}); err == nil {
		t.Fatal("guest instantiated with an unlinked host import — the zero-authority floor is broken")
	}
}

// No Workspace = NO filesystem at all. The fsprobe guest attempts a create inside
// /work and an escaping open, then reports a result byte (bit0 = create-in-/work
// ok, bit1 = escape ok). With a mount it must be 0x01 (write ok, escape blocked);
// with no Workspace it must be 0x00 — every path_open fails because preopen fd 3
// does not exist. The guest handles the WASI errno itself, so it reports rather
// than traps; the security property is that no path is ever opened without a mount.
func TestRun_NoFilesystemGrant_OpenTraps(t *testing.T) {
	tests := []struct {
		name         string
		withWorkable bool
		want         byte
	}{
		{name: "no workspace, no filesystem", withWorkable: false, want: 0x00},
		{name: "confined workspace, write allowed, escape blocked", withWorkable: true, want: 0x01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := sandbox.Config{}
			if tt.withWorkable {
				cfg.Workspace = t.TempDir()
			}
			res, err := sandbox.Run(context.Background(), fsGuest, cfg)
			if err != nil {
				t.Fatalf("Run: %v (stderr=%s)", err, res.Stderr)
			}
			if len(res.Stdout) != 1 || res.Stdout[0] != tt.want {
				t.Fatalf("result = %v, want [%d]", res.Stdout, tt.want)
			}
		})
	}
}

// WASI stdio is wired in all three directions: the stdio guest pipes stdin to
// both stdout and stderr, and both buffers must capture it independently.
func TestRun_StdinStdoutStderr_Piped(t *testing.T) {
	res, err := sandbox.Run(context.Background(), stdioGuest, sandbox.Config{Stdin: []byte("piped")})
	if err != nil {
		t.Fatalf("Run: %v (stderr=%s)", err, res.Stderr)
	}
	if got := string(res.Stdout); got != "piped" {
		t.Fatalf("stdout = %q, want %q", got, "piped")
	}
	if got := string(res.Stderr); got != "piped" {
		t.Fatalf("stderr = %q, want %q", got, "piped")
	}
}
