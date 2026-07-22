package agentkit

import (
	"context"
	"testing"
)

// Same-package coverage for stampFrame and Emit's frame stamping (both read the unexported frame ctx
// value set by withFrame).

func TestStampFrame_AllVariants(t *testing.T) {
	const frame uint64 = 9
	tests := []struct {
		name string
		in   Event
		get  func(Event) uint64
	}{
		{"Token", Token{}, func(e Event) uint64 { return e.(Token).Frame }},
		{"Thinking", Thinking{}, func(e Event) uint64 { return e.(Thinking).Frame }},
		{"ToolStart", ToolStart{}, func(e Event) uint64 { return e.(ToolStart).Frame }},
		{"ToolEnd", ToolEnd{}, func(e Event) uint64 { return e.(ToolEnd).Frame }},
		{"TurnStart", TurnStart{}, func(e Event) uint64 { return e.(TurnStart).Frame }},
		{"TurnEnd", TurnEnd{}, func(e Event) uint64 { return e.(TurnEnd).Frame }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.get(stampFrame(tt.in, frame)); got != frame {
				t.Fatalf("stampFrame(%s).Frame = %d, want %d", tt.name, got, frame)
			}
		})
	}
}

func TestEmit_StampsFrameFromCtx(t *testing.T) {
	var got Event
	ctx := WithSink(withFrame(context.Background(), 7), func(e Event) { got = e })
	Emit(ctx, Token{Text: "x"})
	tok, ok := got.(Token)
	if !ok {
		t.Fatalf("delivered %T, want Token", got)
	}
	if tok.Frame != 7 {
		t.Fatalf("emitted Token.Frame = %d, want 7 (from ctx)", tok.Frame)
	}
}
