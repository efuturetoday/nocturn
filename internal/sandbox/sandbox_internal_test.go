package sandbox

import (
	"context"
	_ "embed"
	"errors"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/sys"

	"github.com/efuturetoday/nocturn/agentkit"
)

// finish and writeToGuest are unexported helpers, so these tests live in the
// package (white-box). The rest of the suite is black-box (package sandbox_test).

// finish maps a run's terminal state to a Result/error. A WASI command's normal
// exit(0) is success; a non-zero exit is reported with its code; a context that
// carried a cause reports that cause (not the opaque trap the guest surfaces as).
func TestFinish(t *testing.T) {
	tests := []struct {
		name       string
		ctx        context.Context
		err        error
		wantErr    bool
		wantIs     error  // errors.Is target, if wantErr
		wantSubstr string // substring of the error message, if wantErr
	}{
		{
			name:    "nil error is success",
			ctx:     context.Background(),
			err:     nil,
			wantErr: false,
		},
		{
			name:    "normal exit(0) is success",
			ctx:     context.Background(),
			err:     sys.NewExitError(0),
			wantErr: false,
		},
		{
			name:       "non-zero exit reports the code",
			ctx:        context.Background(),
			err:        sys.NewExitError(3),
			wantErr:    true,
			wantSubstr: "guest exited with code 3",
		},
		{
			name:       "context cause wins over the opaque trap",
			ctx:        canceledCtx(agentkit.ErrTurnTimeout),
			err:        sys.NewExitError(0xEFFFFFFF), // wazero's context-done trap code
			wantErr:    true,
			wantIs:     agentkit.ErrTurnTimeout,
			wantSubstr: "run halted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := finish(tt.ctx, []byte("out"), []byte("err"), tt.err)
			if string(res.Stdout) != "out" || string(res.Stderr) != "err" {
				t.Fatalf("Result = %q/%q, want output carried through unchanged", res.Stdout, res.Stderr)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatalf("err = nil, want an error")
				}
				if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
					t.Fatalf("err = %v, want it to wrap %v", err, tt.wantIs)
				}
				if tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
					t.Fatalf("err = %q, want it to contain %q", err, tt.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}

// writeToGuest returns 0 (a "no response" packed pointer) for an empty payload —
// before it even inspects the module — and when the guest exports no usable
// malloc. The nomalloc guest is memory-only, so its ExportedFunction("malloc") is
// nil; a non-empty write must still not panic and must return 0.
func TestWriteToGuest_NoUsableAllocator_ReturnsZero(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, nomallocGuest)
	if err != nil {
		t.Fatalf("instantiate nomalloc guest: %v", err)
	}

	if got := writeToGuest(ctx, mod, nil); got != 0 {
		t.Fatalf("empty payload: writeToGuest = %d, want 0", got)
	}
	if got := writeToGuest(ctx, mod, []byte("payload")); got != 0 {
		t.Fatalf("no malloc export: writeToGuest = %d, want 0", got)
	}
}

// canceledCtx returns a context already cancelled with the given cause, for
// exercising finish's context-cause branch.
func canceledCtx(cause error) context.Context {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	return ctx
}

//go:embed testdata/nomalloc.wasm
var nomallocGuest []byte
