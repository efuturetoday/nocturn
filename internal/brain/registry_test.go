package brain_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/brain"
)

// The Registry emits exactly one ToolStart before and one ToolEnd after the
// tool runs, carrying the caller's args and the tool's result.
func TestRegistry_InvokeEmitsStartThenEnd(t *testing.T) {
	var events []brain.ToolEvent
	reg := brain.NewRegistry([]brain.Tool{
		tool("echo", func(_ context.Context, args string) (string, error) { return "OUT:" + args, nil }),
	})
	reg.OnCall = func(ev brain.ToolEvent) { events = append(events, ev) }

	out, err := reg.Invoke(context.Background(), "echo", `{"a":1}`)
	if err != nil || out != `OUT:{"a":1}` {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (start+end)", len(events))
	}
	if events[0].Phase != brain.ToolStart || events[0].Tool != "echo" || events[0].Args != `{"a":1}` {
		t.Fatalf("start event = %+v", events[0])
	}
	if events[1].Phase != brain.ToolEnd || events[1].Result != `OUT:{"a":1}` || events[1].Err != nil {
		t.Fatalf("end event = %+v", events[1])
	}
}

// An unknown tool is reported as the ToolEnd's Err (and as the returned error),
// not fatal — the caller can surface it.
func TestRegistry_UnknownToolReportedInEndEvent(t *testing.T) {
	var events []brain.ToolEvent
	reg := brain.NewRegistry(nil)
	reg.OnCall = func(ev brain.ToolEvent) { events = append(events, ev) }

	if _, err := reg.Invoke(context.Background(), "ghost", "{}"); err == nil ||
		!strings.Contains(err.Error(), "unknown tool ghost") {
		t.Fatalf("err = %v, want unknown tool ghost", err)
	}
	if len(events) != 2 || events[1].Phase != brain.ToolEnd || events[1].Err == nil {
		t.Fatalf("events = %+v", events)
	}
}

// A nil observer is a no-op: dispatch still works.
func TestRegistry_NilObserverIsNoOp(t *testing.T) {
	reg := brain.NewRegistry([]brain.Tool{
		tool("echo", func(context.Context, string) (string, error) { return "ok", nil }),
	})
	if out, err := reg.Invoke(context.Background(), "echo", "{}"); err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// A tool whose Invoke dispatches back into the Registry — exactly how the script
// interpreter's code.run reaches effects — produces nested Start/End events
// framed by the parent's, which is what lets the UI nest script effects under
// their code.run.
func TestRegistry_NestedCallsNestByOrder(t *testing.T) {
	var events []string
	reg := brain.NewRegistry(nil)
	reg.OnCall = func(ev brain.ToolEvent) {
		phase := "start"
		if ev.Phase == brain.ToolEnd {
			phase = "end"
		}
		events = append(events, ev.Tool+":"+phase)
	}
	reg.Add(tool("leaf", func(context.Context, string) (string, error) { return "L", nil }))
	reg.Add(tool("parent", func(ctx context.Context, _ string) (string, error) {
		_, _ = reg.Invoke(ctx, "leaf", "{}")
		_, _ = reg.Invoke(ctx, "leaf", "{}")
		return "P", nil
	}))

	if _, err := reg.Invoke(context.Background(), "parent", "{}"); err != nil {
		t.Fatalf("invoke parent: %v", err)
	}
	want := []string{"parent:start", "leaf:start", "leaf:end", "leaf:start", "leaf:end", "parent:end"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v\nwant %v", events, want)
	}
}
