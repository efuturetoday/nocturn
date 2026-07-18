// Package appserver is the server side of the companion-app protocol: it bridges one
// chat.Chat (the headless turn loop) to a remote client over a duplex byte stream,
// so a mobile/desktop app drives a workspace's chat exactly like the TUI does — Submit
// in, events out, approvals answered in-app.
//
// It is transport-agnostic: the Bridge speaks to an abstract Conn (framed []byte
// messages), so the WebSocket library lives only in the daemon entrypoint and a future
// relay is just a different Conn. The wire format is JSON: a tagged event object per
// chat.Event out, a tagged command object in. Nothing here reaches an effect — every
// command routes through the Runner, so the broker + HITL still gate everything.
package appserver

import (
	"encoding/json"
	"fmt"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/chat"
)

// --- events (server → client) ------------------------------------------------

// wireEvent is the flat, tagged JSON shape for one chat.Event. Type discriminates
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

// EncodeEvent renders a chat.Event as a tagged JSON message. An unknown event type is
// an error (a new event must be added here deliberately — the client contract is explicit).
func EncodeEvent(e chat.Event) ([]byte, error) {
	var w wireEvent
	switch ev := e.(type) {
	case chat.TokenEvent:
		w = wireEvent{Type: "token", Text: ev.Text}
	case chat.ThinkingEvent:
		w = wireEvent{Type: "thinking", Text: ev.Text}
	case chat.TurnStartEvent:
		w = wireEvent{Type: "turnStart", Display: ev.Display, Input: ev.Input, Source: string(ev.Source)}
	case chat.TurnEndEvent:
		w = wireEvent{Type: "turnEnd", Answer: ev.Answer, Err: errString(ev.Err)}
	case chat.QueuedEvent:
		w = wireEvent{Type: "queued", Display: ev.Display, Input: ev.Input, Source: string(ev.Source)}
	case chat.NoticeEvent:
		w = wireEvent{Type: "notice", Text: ev.Text, IsErr: ev.Err}
	case chat.ApprovalEvent:
		w = wireEvent{Type: "approval", ID: ev.ID, Intent: ev.Intent, Options: ev.Options}
	case chat.ApprovalResolvedEvent:
		w = wireEvent{Type: "approvalResolved", ID: ev.ID}
	case chat.ToolEvent:
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
	Type     string            `json:"type"` // always "snapshot"
	Running  bool              `json:"running"`
	Queue    []wireQueued      `json:"queue"`
	Messages []wireMessage     `json:"messages"`
	Forest   []wireForestFrame `json:"forest,omitempty"`  // the full completed tool tree (sub-calls + errors), in stream order
	Pending  *wireEvent        `json:"pending,omitempty"` // an unanswered approval, or null
}

// wireForestFrame is one completed tool invocation from the persisted forest: id/parent give
// the call tree (parent 0 = a model-issued top-level call; a nested effect carries its
// enclosing call's id), result/err the outcome. Unlike the per-message `tools` (a flat
// top-level view reconstructed from history for placement), the forest lets the client
// rebuild the EXACT tree it saw live — sub-calls and their errors included.
type wireForestFrame struct {
	ID     uint64 `json:"id"`
	Parent uint64 `json:"parent"`
	Tool   string `json:"tool"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Err    string `json:"err,omitempty"`
}

type wireQueued struct {
	Display string `json:"display"`
	Input   string `json:"input"`
	Source  string `json:"source"`
}

type wireMessage struct {
	Role    string         `json:"role"`
	Content string         `json:"content"`
	Tools   []wireSnapTool `json:"tools,omitempty"` // assistant turn: the tool calls it made (static, from history)
}

// wireSnapTool is one tool call reconstructed from saved history for the snapshot — the
// name, the arguments, and the result it got. Unlike the live ToolEvent it carries no
// id/parent/phase: it is a finished call in the transcript, not a streaming frame.
type wireSnapTool struct {
	Tool   string `json:"tool"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
}

// EncodeSnapshot renders a Runner snapshot — the state a joining or reconnecting client
// needs to render the conversation, the running flag, the buffered queue, and any pending
// approval — as one tagged message.
func EncodeSnapshot(s chat.Snapshot) ([]byte, error) {
	w := wireSnapshot{Type: "snapshot", Running: s.Running}
	for _, q := range s.Queue {
		w.Queue = append(w.Queue, wireQueued{Display: q.Display, Input: q.Input, Source: string(q.Source)})
	}
	// Tool results live in role=tool messages, tied to their call by ToolCallID; index them
	// so an assistant turn's calls can carry their result inline (the client renders a static
	// tool forest under the bubble, matching what the live stream showed when it ran).
	results := map[string]string{}
	for _, m := range s.Messages {
		if m.Role == "tool" {
			results[m.ToolCallID] = m.Content
		}
	}
	for _, m := range s.Messages {
		if m.Role != "user" && m.Role != "assistant" { // system/tool plumbing is not a bubble
			continue
		}
		wm := wireMessage{Role: m.Role, Content: m.Content}
		for _, tc := range m.ToolCalls {
			wm.Tools = append(wm.Tools, wireSnapTool{Tool: tc.Tool, Args: tc.Args, Result: results[tc.ID]})
		}
		if wm.Content == "" && len(wm.Tools) == 0 {
			continue // an empty assistant turn with no calls carries nothing to render
		}
		w.Messages = append(w.Messages, wm)
	}
	// The full persisted tool forest — the client rebuilds the exact live tree (sub-calls +
	// errors), which the flat per-message `tools` can't carry.
	for _, f := range s.Forest {
		w.Forest = append(w.Forest, wireForestFrame{ID: f.ID, Parent: f.Parent, Tool: f.Tool, Args: f.Args, Result: f.Result, Err: f.Err})
	}
	if s.Pending != nil {
		w.Pending = &wireEvent{Type: "approval", ID: s.Pending.ID, Intent: s.Pending.Intent, Options: s.Pending.Options}
	}
	return json.Marshal(w)
}

// --- commands (client → server) ----------------------------------------------

type wireCommand struct {
	Cmd     string `json:"cmd"`
	WS      string `json:"ws,omitempty"`      // workspace: getWorkspace, setPersona, all chat cmds
	ID      string `json:"id,omitempty"`      // chat id (openChat/renameChat/deleteChat) OR approval id (resolve)
	Name    string `json:"name,omitempty"`    // chat name (newChat, renameChat)
	Text    string `json:"text,omitempty"`    // setPersona
	Input   string `json:"input,omitempty"`   // submit, submitSkill
	Display string `json:"display,omitempty"` // submitSkill, submitAgent
	Agent   string `json:"agent,omitempty"`   // submitAgent
	Task    string `json:"task,omitempty"`    // submitAgent
	Choice  int    `json:"choice,omitempty"`  // resolve
}

// decodeCommand parses one client message into a command. The server switches on Cmd.
func decodeCommand(msg []byte) (wireCommand, error) {
	var c wireCommand
	if err := json.Unmarshal(msg, &c); err != nil {
		return wireCommand{}, fmt.Errorf("appserver: bad command json: %w", err)
	}
	return c, nil
}

// routeChatCommand drives the OPEN workspace's Runner for a chat command; an unknown cmd
// is ignored (the server already handled control commands). Every command that runs an
// effect goes through the Runner, so the broker + HITL still gate it.
func routeChatCommand(r Runner, c wireCommand) {
	switch c.Cmd {
	case "submit":
		r.Submit(chat.SourceUser, c.Input)
	case "submitSkill":
		r.SubmitInput(chat.SourceUser, c.Display, c.Input)
	case "submitAgent":
		r.SubmitAgent(c.Display, c.Agent, c.Task)
	case "cancel":
		r.Cancel()
	case "reset":
		r.Reset()
	case "resolve":
		r.Resolve(c.ID, c.Choice)
	}
}

// --- control replies (server → client) ---------------------------------------

// encodeWorkspaces / encodeWorkspace / encodeError render the control-plane replies. They
// marshal plain structs (no channels/funcs), so json.Marshal cannot fail — the error is
// deliberately ignored (see decisions.md §Handle errors).

func encodeWorkspaces(items []WorkspaceSummary) []byte {
	b, _ := json.Marshal(struct {
		Type  string             `json:"type"`
		Items []WorkspaceSummary `json:"items"`
	}{"workspaces", items})
	return b
}

func encodeWorkspace(st WorkspaceState) []byte {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		WorkspaceState
	}{"workspace", st})
	return b
}

func encodeChats(ws string, items []ChatMeta) []byte {
	b, _ := json.Marshal(struct {
		Type  string     `json:"type"`
		WS    string     `json:"ws"`
		Items []ChatMeta `json:"items"`
	}{"chats", ws, items})
	return b
}

func encodeChatActivity(a ChatActivity) []byte {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		ChatActivity
	}{"chatActivity", a})
	return b
}

func encodeError(text string) []byte {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"error", text})
	return b
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
