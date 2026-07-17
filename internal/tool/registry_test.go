package tool_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// mkTool builds a bare tool.Tool with the given name and Invoke.
func mkTool(name string, invoke func(context.Context, string) (string, error)) tool.Tool {
	return tool.Tool{Spec: tool.Spec{Name: name}, Invoke: invoke}
}

// sink returns a ctx carrying a stream sink that records every ToolEvent, plus the
// slice it fills — how a consumer observes tool calls now (via ctx, not a field).
func sink(ctx context.Context, events *[]activity.ToolEvent) context.Context {
	return activity.WithSink(ctx, func(e activity.Event) {
		if te, ok := e.(activity.ToolEvent); ok {
			*events = append(*events, te)
		}
	})
}

// The Registry emits exactly one Start before and one End after the tool runs,
// carrying the caller's args and the tool's result.
func TestRegistry_InvokeEmitsStartThenEnd(t *testing.T) {
	var events []activity.ToolEvent
	reg := tool.NewRegistry([]tool.Tool{
		mkTool("echo", func(_ context.Context, args string) (string, error) { return "OUT:" + args, nil }),
	})
	ctx := sink(context.Background(), &events)

	out, err := reg.Invoke(ctx, "echo", `{"a":1}`)
	if err != nil || out != `OUT:{"a":1}` {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (start+end)", len(events))
	}
	if events[0].Phase != activity.Start || events[0].Tool != "echo" || events[0].Args != `{"a":1}` {
		t.Fatalf("start event = %+v", events[0])
	}
	if events[1].Phase != activity.End || events[1].Result != `OUT:{"a":1}` || events[1].Err != nil {
		t.Fatalf("end event = %+v", events[1])
	}
}

// An unknown tool is reported as the End event's Err (and as the returned error),
// not fatal — the caller can surface it.
func TestRegistry_UnknownToolReportedInEndEvent(t *testing.T) {
	var events []activity.ToolEvent
	reg := tool.NewRegistry(nil)
	ctx := sink(context.Background(), &events)

	if _, err := reg.Invoke(ctx, "ghost", "{}"); err == nil ||
		!strings.Contains(err.Error(), "unknown tool ghost") {
		t.Fatalf("err = %v, want unknown tool ghost", err)
	}
	if len(events) != 2 || events[1].Phase != activity.End || events[1].Err == nil {
		t.Fatalf("events = %+v", events)
	}
}

// A ctx with no sink is a no-op: dispatch still works (a detached run is silent).
func TestRegistry_NoSinkIsNoOp(t *testing.T) {
	reg := tool.NewRegistry([]tool.Tool{
		mkTool("echo", func(context.Context, string) (string, error) { return "ok", nil }),
	})
	if out, err := reg.Invoke(context.Background(), "echo", "{}"); err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// A tool whose Invoke dispatches back into the Registry — exactly how the script
// interpreter's code.run reaches effects — produces nested Start/End events framed
// by the parent's, which is what lets the UI nest script effects under their
// code.run. The nested call inherits the sink from ctx automatically.
func TestRegistry_NestedCallsNestByOrder(t *testing.T) {
	var events []string
	reg := tool.NewRegistry(nil)
	ctx := activity.WithSink(context.Background(), func(e activity.Event) {
		ev, ok := e.(activity.ToolEvent)
		if !ok {
			return
		}
		phase := "start"
		if ev.Phase == activity.End {
			phase = "end"
		}
		events = append(events, ev.Tool+":"+phase)
	})
	reg.Add(mkTool("leaf", func(context.Context, string) (string, error) { return "L", nil }))
	reg.Add(mkTool("parent", func(ctx context.Context, _ string) (string, error) {
		_, _ = reg.Invoke(ctx, "leaf", "{}")
		_, _ = reg.Invoke(ctx, "leaf", "{}")
		return "P", nil
	}))

	if _, err := reg.Invoke(ctx, "parent", "{}"); err != nil {
		t.Fatalf("invoke parent: %v", err)
	}
	want := []string{"parent:start", "leaf:start", "leaf:end", "leaf:start", "leaf:end", "parent:end"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v\nwant %v", events, want)
	}
}
