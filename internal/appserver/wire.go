// Package appserver is the server side of the companion-app protocol: it bridges one
// session.Runner (the headless turn loop) to a remote client over a duplex byte stream,
// so a mobile/desktop app drives a workspace's chat exactly like the TUI does — Submit
// in, events out, approvals answered in-app.
//
// It is transport-agnostic: the Bridge speaks to an abstract Conn (framed []byte
// messages), so the WebSocket library lives only in the daemon entrypoint and a future
// relay is just a different Conn. The wire format is JSON: a tagged event object per
// session.Event out, a tagged command object in. Nothing here reaches an effect — every
// command routes through the Runner, so the broker + HITL still gate everything.
package appserver

import (
	"encoding/json"
	"fmt"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/session"
)

// --- events (server → client) ------------------------------------------------

// wireEvent is the flat, tagged JSON shape for one session.Event. Type discriminates
// the union; only the fields relevant to that Type are set (omitempty keeps the wire
// small). A TS client switches on `type`.
type wireEvent struct {
	Type    string    `json:"type"`
	Text    string    `json:"text,omitempty"`    // token, thinking
	Display string    `json:"display,omitempty"` // turnStart, queued
	Input   string    `json:"input,omitempty"`   // turnStart, queued
	Source  string    `json:"source,omitempty"`  // turnStart, queued
	Answer  string    `json:"answer,omitempty"`  // turnEnd
	Err     string    `json:"err,omitempty"`     // turnEnd
	IsErr   bool      `json:"isErr,omitempty"`   // notice
	ID      string    `json:"id,omitempty"`      // approval, approvalResolved
	Intent  string    `json:"intent,omitempty"`  // approval
	Options []string  `json:"options,omitempty"` // approval
	Tool    *wireTool `json:"tool,omitempty"`    // tool
}

// wireTool is the observable tool-call frame for a ToolEvent.
type wireTool struct {
	ID     uint64 `json:"id"`
	Parent uint64 `json:"parent"`
	Tool   string `json:"tool"`
	Args   string `json:"args,omitempty"`
	Phase  string `json:"phase"` // "start" | "end"
	Result string `json:"result,omitempty"`
	Err    string `json:"err,omitempty"`
}

// EncodeEvent renders a session.Event as a tagged JSON message. An unknown event type is
// an error (a new event must be added here deliberately — the client contract is explicit).
func EncodeEvent(e session.Event) ([]byte, error) {
	var w wireEvent
	switch ev := e.(type) {
	case session.TokenEvent:
		w = wireEvent{Type: "token", Text: ev.Text}
	case session.ThinkingEvent:
		w = wireEvent{Type: "thinking", Text: ev.Text}
	case session.TurnStartEvent:
		w = wireEvent{Type: "turnStart", Display: ev.Display, Input: ev.Input, Source: string(ev.Source)}
	case session.TurnEndEvent:
		w = wireEvent{Type: "turnEnd", Answer: ev.Answer, Err: errString(ev.Err)}
	case session.QueuedEvent:
		w = wireEvent{Type: "queued", Display: ev.Display, Input: ev.Input, Source: string(ev.Source)}
	case session.NoticeEvent:
		w = wireEvent{Type: "notice", Text: ev.Text, IsErr: ev.Err}
	case session.ApprovalEvent:
		w = wireEvent{Type: "approval", ID: ev.ID, Intent: ev.Intent, Options: ev.Options}
	case session.ApprovalResolvedEvent:
		w = wireEvent{Type: "approvalResolved", ID: ev.ID}
	case session.ToolEvent:
		phase := "start"
		if ev.Event.Phase == activity.End {
			phase = "end"
		}
		w = wireEvent{Type: "tool", Tool: &wireTool{
			ID: ev.Event.ID, Parent: ev.Event.Parent, Tool: ev.Event.Tool,
			Args: ev.Event.Args, Phase: phase, Result: ev.Event.Result, Err: errString(ev.Event.Err),
		}}
	default:
		return nil, fmt.Errorf("appserver: cannot encode event %T", e)
	}
	return json.Marshal(w)
}

// --- snapshot (server → client, on connect / reconnect) ----------------------

type wireSnapshot struct {
	Type     string        `json:"type"` // always "snapshot"
	Running  bool          `json:"running"`
	Queue    []wireQueued  `json:"queue"`
	Messages []wireMessage `json:"messages"`
	Pending  *wireEvent    `json:"pending,omitempty"` // an unanswered approval, or null
}

type wireQueued struct {
	Display string `json:"display"`
	Input   string `json:"input"`
	Source  string `json:"source"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// EncodeSnapshot renders a Runner snapshot — the state a joining or reconnecting client
// needs to render the conversation, the running flag, the buffered queue, and any pending
// approval — as one tagged message.
func EncodeSnapshot(s session.Snapshot) ([]byte, error) {
	w := wireSnapshot{Type: "snapshot", Running: s.Running}
	for _, q := range s.Queue {
		w.Queue = append(w.Queue, wireQueued{Display: q.Display, Input: q.Input, Source: string(q.Source)})
	}
	for _, m := range s.Messages {
		if m.Role == "user" || m.Role == "assistant" { // system/tool plumbing is not client-facing
			w.Messages = append(w.Messages, wireMessage{Role: m.Role, Content: m.Content})
		}
	}
	if s.Pending != nil {
		w.Pending = &wireEvent{Type: "approval", ID: s.Pending.ID, Intent: s.Pending.Intent, Options: s.Pending.Options}
	}
	return json.Marshal(w)
}

// --- commands (client → server) ----------------------------------------------

type wireCommand struct {
	Cmd     string `json:"cmd"`
	Input   string `json:"input,omitempty"`   // submit, submitSkill
	Display string `json:"display,omitempty"` // submitSkill, submitAgent
	Agent   string `json:"agent,omitempty"`   // submitAgent
	Task    string `json:"task,omitempty"`    // submitAgent
	ID      string `json:"id,omitempty"`      // resolve
	Choice  int    `json:"choice,omitempty"`  // resolve
}

// dispatchCommand parses one client message and drives r. An unknown cmd is a protocol
// error (the caller decides whether to close the conn or ignore). Every command that
// runs an effect goes through the Runner, so the broker + HITL still gate it.
func dispatchCommand(r Runner, msg []byte) error {
	var c wireCommand
	if err := json.Unmarshal(msg, &c); err != nil {
		return fmt.Errorf("appserver: bad command json: %w", err)
	}
	switch c.Cmd {
	case "submit":
		r.Submit(session.SourceUser, c.Input)
	case "submitSkill":
		r.SubmitInput(session.SourceUser, c.Display, c.Input)
	case "submitAgent":
		r.SubmitAgent(c.Display, c.Agent, c.Task)
	case "cancel":
		r.Cancel()
	case "reset":
		r.Reset()
	case "resolve":
		r.Resolve(c.ID, c.Choice)
	default:
		return fmt.Errorf("appserver: unknown command %q", c.Cmd)
	}
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
