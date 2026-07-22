package agentkit_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
)

// Tool-bus behaviour over the public API. The ctx-carried id/frame plumbing (nextCallID, withCounter,
// withFrame, truncateChars) is exercised same-package in tool_internal_test.go.

func TestToolSet_Call_AssignsFreshID(t *testing.T) {
	set := newSet(t, echoTool(t, "echo", "R"))
	cs := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), cs.fn())
	// Two sequential tool-call steps → ids 1 then 2 (monotonic from the ctx counter installed by Once).
	llm := &stepLLM{steps: []agentkit.Step{
		callStep("a", "echo", "{}"),
		callStep("b", "echo", "{}"),
		answerStep("done"),
	}}
	if _, err := agentkit.Once(ctx, llm, "go", agentkit.WithTools(set)); err != nil {
		t.Fatalf("Once err = %v", err)
	}
	starts := toolStartEvents(cs.all())
	if len(starts) != 2 || starts[0].ID != 1 || starts[1].ID != 2 {
		t.Fatalf("ToolStart ids = %v, want [1 2]", starts)
	}
}

func TestToolSet_Call_EmitsStartEnd_FrameIsParent(t *testing.T) {
	set := newSet(t, echoTool(t, "echo", "R"))
	cs := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), cs.fn())
	llm := &stepLLM{steps: []agentkit.Step{callStep("a", "echo", "{}"), answerStep("done")}}
	if _, err := agentkit.Once(ctx, llm, "go", agentkit.WithTools(set)); err != nil {
		t.Fatalf("Once err = %v", err)
	}
	starts, ends := toolStartEvents(cs.all()), toolEndEvents(cs.all())
	if len(starts) != 1 || starts[0].Frame != 0 || starts[0].ID != 1 {
		t.Fatalf("ToolStart = %+v, want {Frame:0 ID:1}", starts)
	}
	if len(ends) != 1 || ends[0].Frame != 0 || ends[0].ID != 1 {
		t.Fatalf("ToolEnd = %+v, want {Frame:0 ID:1}", ends)
	}
}

func TestToolSet_Call_ChildFrameNests(t *testing.T) {
	// The tool reads FrameFrom(ctx): it must equal its OWN call id, since it runs under withFrame(id).
	probe := newTool(t, "probe", func(ctx context.Context, _ string) (string, error) {
		return strconv.FormatUint(agentkit.FrameFrom(ctx), 10), nil
	})
	set := newSet(t, probe)
	store := &fakeStore{}
	llm := &stepLLM{steps: []agentkit.Step{callStep("a", "probe", "{}"), answerStep("done")}}
	if _, err := agentkit.Once(context.Background(), llm, "go",
		agentkit.WithTools(set), agentkit.WithStore(store, "s")); err != nil {
		t.Fatalf("Once err = %v", err)
	}
	if c := toolResult(t, store.history()).Content; c != "1" {
		t.Fatalf("FrameFrom inside tool = %q, want its own call id 1", c)
	}
}

func TestToolSet_Call_UnknownTool_NonFatalError(t *testing.T) {
	set := newSet(t, echoTool(t, "known", "R"))
	cs := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), cs.fn())
	out, err := set.Call(ctx, "missing", "{}")
	if out != "" || err == nil || !strings.Contains(err.Error(), `unknown tool "missing"`) {
		t.Fatalf("Call = (%q, %v), want unknown-tool error", out, err)
	}
	if ev := cs.all(); len(ev) != 0 {
		t.Fatalf("emitted %d events for an unknown tool, want none", len(ev))
	}
}

func TestToolSet_Call_DurationExcludesApprovalWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tool := newTool(t, "slow", func(ctx context.Context, _ string) (string, error) {
			time.Sleep(10 * time.Millisecond) // active work
			resume := agentkit.Pause(ctx)
			time.Sleep(200 * time.Millisecond) // out-of-band approval wait — must be excluded
			resume()
			time.Sleep(10 * time.Millisecond) // more active work
			return "ok", nil
		})
		set := newSet(t, tool)
		cs := &captureSink{}
		ctx := agentkit.WithSink(context.Background(), cs.fn())
		llm := &stepLLM{steps: []agentkit.Step{callStep("a", "slow", "{}"), answerStep("done")}}
		if _, err := agentkit.Once(ctx, llm, "go", agentkit.WithTools(set)); err != nil {
			t.Fatalf("Once err = %v", err)
		}
		ends := toolEndEvents(cs.all())
		if len(ends) != 1 {
			t.Fatalf("ToolEnd events = %d, want 1", len(ends))
		}
		if d := ends[0].Duration; d >= 200*time.Millisecond {
			t.Fatalf("ToolEnd.Duration = %v, want it to exclude the 200ms approval wait", d)
		}
	})
}

func TestFrameFrom_TopLevelZero(t *testing.T) {
	if got := agentkit.FrameFrom(context.Background()); got != 0 {
		t.Fatalf("FrameFrom(bg) = %d, want 0", got)
	}
}

func TestNewTool_ValidatesName(t *testing.T) {
	long := strings.Repeat("a", 65)
	tests := []struct {
		name    string
		toolNam string
		wantErr bool
	}{
		{"valid", "read_file-1", false},
		{"empty", "", true},
		{"too long", long, true},
		{"bad char", "bad name!", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := agentkit.NewTool(tt.toolNam, "desc",
				func(context.Context, string) (string, error) { return "", nil })
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewTool(%q) err = %v, wantErr %v", tt.toolNam, err, tt.wantErr)
			}
		})
	}
}

func TestNewTool_NilFunc_Error(t *testing.T) {
	if _, err := agentkit.NewTool("ok", "desc", nil); err == nil {
		t.Fatal("NewTool with nil func: err = nil, want error")
	}
}

func TestNewToolSet_DuplicateName_Error(t *testing.T) {
	if _, err := agentkit.NewToolSet(echoTool(t, "dup", "1"), echoTool(t, "dup", "2")); err == nil {
		t.Fatal("duplicate tool name: err = nil, want error")
	}
}

func TestNewToolSet_NilTool_Error(t *testing.T) {
	if _, err := agentkit.NewToolSet(echoTool(t, "ok", "1"), nil); err == nil {
		t.Fatal("nil tool: err = nil, want error")
	}
}

func TestNewToolSet_InvalidSpec_Error(t *testing.T) {
	bad := badSpecTool{name: "bad name!"}
	if _, err := agentkit.NewToolSet(bad); err == nil {
		t.Fatal("invalid spec: err = nil, want error")
	}
}

func TestToolSet_Specs_SortedByName(t *testing.T) {
	set := newSet(t, echoTool(t, "charlie", "c"), echoTool(t, "alpha", "a"), echoTool(t, "bravo", "b"))
	specs := set.Specs()
	got := make([]string, len(specs))
	for i, s := range specs {
		got[i] = s.Name
	}
	want := []string{"alpha", "bravo", "charlie"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Specs order = %v, want %v", got, want)
		}
	}
}

func TestToolSet_Select_ReturnsSubsetCopy(t *testing.T) {
	set := newSet(t, echoTool(t, "keep", "k"), echoTool(t, "drop", "d"))
	sub := set.Select(func(name string) bool { return name == "keep" })

	if _, err := sub.Call(context.Background(), "drop", "{}"); err == nil {
		t.Fatal("dropped tool reachable in subset; want unknown-tool error")
	}
	// The original is unchanged — a subset cannot widen the parent.
	if _, err := set.Call(context.Background(), "drop", "{}"); err != nil {
		t.Fatalf("Select mutated the original set: %v", err)
	}
}

func TestFuncTool_Call_MaxCharsTruncation(t *testing.T) {
	t.Run("success path truncates to rune limit", func(t *testing.T) {
		tool := newTool(t, "big", func(context.Context, string) (string, error) {
			return "héllo", nil
		}, agentkit.WithMaxChars(3))
		out, err := tool.Call(context.Background(), "{}")
		if err != nil {
			t.Fatalf("Call err = %v", err)
		}
		if out != "hél" {
			t.Fatalf("truncated = %q, want %q", out, "hél")
		}
	})

	t.Run("error path returns raw output untruncated", func(t *testing.T) {
		tool := newTool(t, "big", func(context.Context, string) (string, error) {
			return "raw long output", errors.New("boom")
		}, agentkit.WithMaxChars(3))
		out, err := tool.Call(context.Background(), "{}")
		if err == nil {
			t.Fatal("want error")
		}
		if out != "raw long output" {
			t.Fatalf("error-path output = %q, want it raw/untruncated", out)
		}
	})
}

func TestToolSpec_Validate_DescriptionLengthNotEnforced(t *testing.T) {
	spec := agentkit.ToolSpec{Name: "ok", Description: strings.Repeat("x", agentkit.MaxToolDescLen+100)}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate over-long description = %v, want nil (length is advisory)", err)
	}
}

func TestToolSet_Call_ToolErrorPropagatedOnToolEnd(t *testing.T) {
	tool := newTool(t, "boom", func(context.Context, string) (string, error) {
		return "", errors.New("kaput")
	})
	set := newSet(t, tool)
	cs := &captureSink{}
	ctx := agentkit.WithSink(context.Background(), cs.fn())
	_, err := set.Call(ctx, "boom", "{}")
	if err == nil {
		t.Fatal("Call err = nil, want the tool error propagated")
	}
	ends := toolEndEvents(cs.all())
	if len(ends) != 1 || ends[0].Err == nil {
		t.Fatalf("ToolEnd = %+v, want Err set", ends)
	}
}

// --- local helpers ---

// badSpecTool is a Tool whose Spec is invalid — used to exercise NewToolSet's validation.
type badSpecTool struct{ name string }

func (b badSpecTool) Spec() agentkit.ToolSpec { return agentkit.ToolSpec{Name: b.name} }
func (b badSpecTool) Call(context.Context, string) (string, error) {
	return "", nil
}
