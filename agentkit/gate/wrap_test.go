package gate_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// recordTool is a minimal agentkit.Tool that records whether its Call ran — enough to prove a gate
// blocked (or allowed) the underlying tool.
type recordTool struct {
	name string

	mu     sync.Mutex
	called int
}

func (r *recordTool) Spec() agentkit.ToolSpec { return agentkit.ToolSpec{Name: r.name} }

func (r *recordTool) Call(_ context.Context, _ string) (string, error) {
	r.mu.Lock()
	r.called++
	r.mu.Unlock()
	return "ran:" + r.name, nil
}

func (r *recordTool) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

// Wrap gates a tool on its own name: a Deny policy for that Kind blocks the call before the underlying
// tool runs.
func TestWrap_GatesOnToolName(t *testing.T) {
	inner := &recordTool{name: "delete"}
	wrapped := gate.Wrap(inner)

	ctx := gate.With(context.Background(), policyReturning(gate.Denied()), gate.NewMemGrants(), nil)
	_, err := wrapped.Call(ctx, "{}")
	if !errors.Is(err, gate.ErrDenied) {
		t.Fatalf("wrapped Call = %v, want ErrDenied", err)
	}
	if n := inner.callCount(); n != 0 {
		t.Fatalf("underlying tool ran %d times when denied, want 0", n)
	}
}

// When the gate allows the action, the wrapper transparently runs the underlying tool and returns its
// result.
func TestWrap_Allowed_CallsUnderlying(t *testing.T) {
	inner := &recordTool{name: "notify"}
	wrapped := gate.Wrap(inner)

	ctx := gate.With(context.Background(), policyReturning(gate.Allowed()), gate.NewMemGrants(), nil)
	out, err := wrapped.Call(ctx, "{}")
	if err != nil {
		t.Fatalf("wrapped Call = %v, want nil", err)
	}
	if out != "ran:notify" {
		t.Fatalf("result %q, want %q", out, "ran:notify")
	}
	if n := inner.callCount(); n != 1 {
		t.Fatalf("underlying tool ran %d times, want 1", n)
	}
}

// WrapAll wraps every tool in a set and leaves the originals untouched: under a Deny policy the wrapped
// tools are blocked while the untouched originals still run.
func TestWrapAll_WrapsEveryTool_OriginalsUnchanged(t *testing.T) {
	notify := &recordTool{name: "notify"}
	del := &recordTool{name: "delete"}
	orig := agentkit.ToolSet{"notify": notify, "delete": del}

	wrapped := gate.WrapAll(orig)
	if len(wrapped) != len(orig) {
		t.Fatalf("wrapped set has %d tools, want %d", len(wrapped), len(orig))
	}

	ctx := gate.With(context.Background(), policyReturning(gate.Denied()), gate.NewMemGrants(), nil)
	for name, tool := range wrapped {
		if _, err := tool.Call(ctx, "{}"); !errors.Is(err, gate.ErrDenied) {
			t.Fatalf("wrapped %q Call = %v, want ErrDenied", name, err)
		}
	}
	if notify.callCount() != 0 || del.callCount() != 0 {
		t.Fatalf("underlying tools ran under Deny: notify=%d delete=%d, want 0/0", notify.callCount(), del.callCount())
	}

	// The originals in the source set are ungated — they run even with a Deny policy in ctx.
	if _, err := orig["notify"].Call(ctx, "{}"); err != nil {
		t.Fatalf("original notify Call = %v, want nil (unchanged, ungated)", err)
	}
	if notify.callCount() != 1 {
		t.Fatalf("original notify ran %d times, want 1", notify.callCount())
	}
}
