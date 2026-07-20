package agentkit

import (
	"context"
	"time"
)

// Event is one item on a session's output stream — raw, time-ordered observations for a UI to
// render as they happen. The set is sealed (unexported marker method) so a consumer switches
// exhaustively over the known variants and no foreign type enters the stream. Aggregation (session
// token totals, latency histograms, cost) is a CONSUMER concern built by draining these.
//
// Every event carries Frame: the enclosing call-frame id — 0 is the top-level (main) agent,
// non-zero is inside that tool call. A subagent runs inside an AgentTool call, so ALL of its events
// carry that call's id as Frame. That makes the main and subagent streams fully differentiable: a
// UI groups events by Frame, renders each non-zero Frame as a nested (collapsible / hideable) card
// under its tool call, and nests to any depth (a subagent's subagent gets its own Frame). Emit
// stamps Frame from ctx — emitters do not set it.
type Event interface{ isEvent() }

// Token is an answer-text delta.
type Token struct {
	Frame uint64
	Text  string
}

// Thinking is a reasoning-text delta.
type Thinking struct {
	Frame uint64
	Text  string
}

// ToolStart is emitted before a tool's Call runs. ID is this call's id; it becomes the Frame of
// everything the call emits underneath (so a subagent tool's ID is the Frame of the subagent).
type ToolStart struct {
	Frame, ID  uint64
	Tool, Args string
}

// ToolEnd carries the outcome of a tool's Call, including its wall-clock Duration (per-call, a
// tool-level number — not turn-level).
type ToolEnd struct {
	Frame, ID          uint64
	Tool, Args, Result string
	Err                error
	Duration           time.Duration
}

// TurnStart / TurnEnd bracket a whole turn. TurnEnd carries the turn's accumulated TokenCount
// (summed across every model round-trip) and the stop reason, if any (Err = ErrMaxSteps /
// ErrTokenLimit / context deadline). For a subagent turn, Frame is the enclosing AgentTool call.
type TurnStart struct{ Frame uint64 }
type TurnEnd struct {
	Frame  uint64
	Err    error
	Tokens TokenCount
}

func (Token) isEvent()     {}
func (Thinking) isEvent()  {}
func (ToolStart) isEvent() {}
func (ToolEnd) isEvent()   {}
func (TurnStart) isEvent() {}
func (TurnEnd) isEvent()   {}

type sinkKey struct{}

// WithSink attaches a sink to ctx. Emit sends events to it. A nil sink leaves ctx unchanged.
func WithSink(ctx context.Context, sink func(Event)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, sinkKey{}, sink)
}

// SinkFrom returns the sink attached to ctx, or nil.
func SinkFrom(ctx context.Context) func(Event) {
	sink, _ := ctx.Value(sinkKey{}).(func(Event))
	return sink
}

// Emit delivers e to the ctx sink, first stamping its Frame from the ctx call-frame; no-op if no
// sink (fail-open). Adapters call it to stream answer/reasoning tokens without knowing the frame.
func Emit(ctx context.Context, e Event) {
	sink := SinkFrom(ctx)
	if sink == nil {
		return
	}
	sink(stampFrame(e, frameFrom(ctx)))
}

// stampFrame sets the enclosing-frame id on an event so a consumer can attribute it to the main
// agent (0) or a nested call. Emit is authoritative — emitters need not set Frame themselves.
func stampFrame(e Event, frame uint64) Event {
	switch ev := e.(type) {
	case Token:
		ev.Frame = frame
		return ev
	case Thinking:
		ev.Frame = frame
		return ev
	case ToolStart:
		ev.Frame = frame
		return ev
	case ToolEnd:
		ev.Frame = frame
		return ev
	case TurnStart:
		ev.Frame = frame
		return ev
	case TurnEnd:
		ev.Frame = frame
		return ev
	default:
		return e
	}
}
