package agentkit_test

import (
	"context"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestEmit_NoSink_NoOp(t *testing.T) {
	// Fail-open: Emit with no sink attached must not panic.
	agentkit.Emit(context.Background(), agentkit.Token{Text: "x"})
}

func TestWithSink_NilSink_CtxUnchanged(t *testing.T) {
	ctx := context.Background()
	if got := agentkit.WithSink(ctx, nil); got != ctx {
		t.Fatal("WithSink(ctx, nil) returned a different ctx, want it unchanged")
	}
}

func TestSinkFrom_RoundTrip(t *testing.T) {
	var got agentkit.Event
	ctx := agentkit.WithSink(context.Background(), func(e agentkit.Event) { got = e })
	sink := agentkit.SinkFrom(ctx)
	if sink == nil {
		t.Fatal("SinkFrom = nil, want the attached sink")
	}
	sink(agentkit.Token{Text: "hi"})
	if tok, ok := got.(agentkit.Token); !ok || tok.Text != "hi" {
		t.Fatalf("sink delivered %v, want Token{hi}", got)
	}
}

func TestEvent_SealedInterface(t *testing.T) {
	// Compile-time proof that all six variants satisfy the sealed Event interface.
	var _ agentkit.Event = agentkit.Token{}
	var _ agentkit.Event = agentkit.Thinking{}
	var _ agentkit.Event = agentkit.ToolStart{}
	var _ agentkit.Event = agentkit.ToolEnd{}
	var _ agentkit.Event = agentkit.TurnStart{}
	var _ agentkit.Event = agentkit.TurnEnd{}
}
