package chat

import "github.com/efuturetoday/nocturn/internal/activity"

// Source is who originated a turn's input — so a client can render a user message,
// a self-wake resumption, and a reminder differently.
type Source string

const (
	SourceUser   Source = "user"
	SourceWake   Source = "wake"
	SourceRemind Source = "remind"
	SourceAgent  Source = "agent" // a child-agent run submitted via SubmitAgent
)

// Event is one thing that happened in a Runner, delivered to every subscriber. It
// is the OUT half of the Runner's port (commands are the IN half): a TUI, a REST/SSE
// server, or a mobile app all render the same events. The set is a closed union —
// type-switch on it.
type Event interface{ isEvent() }

// TokenEvent is one streamed chunk of the assistant's answer.
type TokenEvent struct{ Text string }

// ThinkingEvent is one streamed chunk of the model's reasoning.
type ThinkingEvent struct{ Text string }

// ToolEvent is a tool call's start or end (the observable forest).
type ToolEvent struct{ Event activity.ToolEvent }

// TurnStartEvent marks a turn beginning: Display is the client-facing line to render
// (a typed "/skill …" or "/agent task"), Input is what actually ran (an expanded skill
// body). They differ only when a client submitted them apart; otherwise Display == Input.
type TurnStartEvent struct {
	Display string
	Input   string
	Source  Source
}

// TurnEndEvent marks a turn finishing, with its final answer or error.
type TurnEndEvent struct {
	Answer string
	Err    error
}

// QueuedEvent marks an input buffered while a turn was running (type-ahead, or a
// wake/remind that fired mid-turn); it will run when the current turn ends. Display is
// the client-facing line (see TurnStartEvent).
type QueuedEvent struct {
	Display string
	Input   string
	Source  Source
}

// NoticeEvent is a dim system line (a reset happened, a background scheduler line).
type NoticeEvent struct {
	Text string
	Err  bool
}

// ApprovalEvent asks the user to approve an effect out of band: the human-readable
// intent plus the choice labels (the fact line is already folded into intent by the
// gateway). A client renders it and answers with Resolve(ID, choiceIndex). The same
// pending request can also be answered out of band (phone) — same decision, other
// transport. Options are LABELS only; the signed tokens stay host-side.
type ApprovalEvent struct {
	ID      string
	Intent  string
	Options []string
}

// ApprovalResolvedEvent tells other subscribers (a second device) that a pending
// approval was answered, so they can clear the prompt.
type ApprovalResolvedEvent struct{ ID string }

func (TokenEvent) isEvent()            {}
func (ThinkingEvent) isEvent()         {}
func (ToolEvent) isEvent()             {}
func (TurnStartEvent) isEvent()        {}
func (TurnEndEvent) isEvent()          {}
func (QueuedEvent) isEvent()           {}
func (NoticeEvent) isEvent()           {}
func (ApprovalEvent) isEvent()         {}
func (ApprovalResolvedEvent) isEvent() {}
