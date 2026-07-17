package agent

import "github.com/efuturetoday/nocturn/internal/tool"

// Source is who originated a turn's input — so a client can render a user message,
// a self-wake resumption, and a reminder differently.
type Source string

const (
	SourceUser   Source = "user"
	SourceWake   Source = "wake"
	SourceRemind Source = "remind"
)

// Event is one thing that happened in a Runner, delivered to every subscriber. It
// is the OUT half of the Runner's port (commands are the IN half): a TUI, a REST/SSE
// server, or a mobile app all render the same events. The set is a closed union —
// type-switch on it.
type Event interface{ isEvent() }

// TokenEvent is one streamed chunk of the assistant's answer.
type TokenEvent struct{ Text string }

// ToolEvent is a tool call's start or end (the observable forest).
type ToolEvent struct{ Event tool.Event }

// TurnStartEvent marks a turn beginning with its input and where it came from.
type TurnStartEvent struct {
	Input  string
	Source Source
}

// TurnEndEvent marks a turn finishing, with its final answer or error.
type TurnEndEvent struct {
	Answer string
	Err    error
}

// QueuedEvent marks an input buffered while a turn was running (type-ahead, or a
// wake/remind that fired mid-turn); it will run when the current turn ends.
type QueuedEvent struct {
	Input  string
	Source Source
}

// NoticeEvent is a dim system line (a reset happened, a background scheduler line).
type NoticeEvent struct {
	Text string
	Err  bool
}

func (TokenEvent) isEvent()     {}
func (ToolEvent) isEvent()      {}
func (TurnStartEvent) isEvent() {}
func (TurnEndEvent) isEvent()   {}
func (QueuedEvent) isEvent()    {}
func (NoticeEvent) isEvent()    {}
