package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/chat"
)

// ── client → server (cmd) ────────────────────────────────────────────────────

// ChatSubmit sends a message to the active chat (starting a new one if none is active).
type ChatSubmit struct {
	Cmd  string `json:"cmd"`
	Text string `json:"text"`
}

// ChatOpen resumes a chat: the server replies with a ChatSnapshot, then streams its turns.
type ChatOpen struct {
	Cmd string `json:"cmd"`
	ID  string `json:"id"`
}

// ChatList requests the workspace's chat list.
type ChatList struct {
	Cmd string `json:"cmd"`
}

// ── server → client (type) ───────────────────────────────────────────────────

// ChatToken is one answer-text delta.
type ChatToken struct {
	Type  string `json:"type"`
	Frame uint64 `json:"frame"`
	Text  string `json:"text"`
}

// ChatThinking is one reasoning-text delta.
type ChatThinking struct {
	Type  string `json:"type"`
	Frame uint64 `json:"frame"`
	Text  string `json:"text"`
}

// ChatTool is a tool call's start or end.
type ChatTool struct {
	Type   string `json:"type"`
	Phase  string `json:"phase"` // "start" | "end"
	Frame  uint64 `json:"frame"`
	ID     uint64 `json:"id"`
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Result string `json:"result,omitempty"`
	Err    string `json:"err,omitempty"`
}

// ChatTurnEnd closes a turn, with the stop reason (if any) and the turn's total tokens.
type ChatTurnEnd struct {
	Type   string `json:"type"`
	Frame  uint64 `json:"frame"`
	Err    string `json:"err,omitempty"`
	Tokens int    `json:"tokens"`
}

// ChatSnapshot is a chat's persisted transcript, sent on open so the client can render it.
type ChatSnapshot struct {
	Type     string             `json:"type"`
	ID       string             `json:"id"`
	Messages []agentkit.Message `json:"messages"`
}

// ChatListResult is the chat list, replying to chat.list.
type ChatListResult struct {
	Type  string      `json:"type"`
	Chats []chat.Meta `json:"chats"`
}

// chat dispatches a chat.* action.
func (c *conn) chat(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "chat.submit":
		var m ChatSubmit
		_ = json.Unmarshal(data, &m)
		c.chatSubmit(ctx, m)
	case "chat.open":
		var m ChatOpen
		_ = json.Unmarshal(data, &m)
		c.chatOpen(ctx, m)
	case "chat.list":
		c.chatList()
	default:
		c.send(Error{Type: "error", Text: "unknown action: " + cmd})
	}
}

func (c *conn) chatSubmit(ctx context.Context, m ChatSubmit) {
	if c.active == nil {
		_, sess := c.space.Chats().Start(ctx, m.Text)
		c.activate(sess)
		return
	}
	c.active.Submit(m.Text)
}

func (c *conn) chatOpen(ctx context.Context, m ChatOpen) {
	msgs, err := c.space.Chats().Transcript(m.ID)
	if err != nil {
		c.send(Error{Type: "error", Text: "open: " + err.Error()})
		return
	}
	c.send(ChatSnapshot{Type: "chat.snapshot", ID: m.ID, Messages: msgs})
	c.activate(c.space.Chats().Open(ctx, m.ID))
}

func (c *conn) chatList() {
	metas, err := c.space.Chats().List()
	if err != nil {
		c.send(Error{Type: "error", Text: "list: " + err.Error()})
		return
	}
	c.send(ChatListResult{Type: "chat.list", Chats: metas})
}

// activate makes sess the connection's live chat: the previous one is closed (its transcript is
// persisted and reloads on open), and a render goroutine forwards the new session's events.
func (c *conn) activate(sess *agentkit.Session) {
	if c.active != nil {
		c.active.Close()
	}
	c.active = sess
	go c.render(sess)
}

// render forwards one session's event stream to the client as chat.* events until it closes.
func (c *conn) render(sess *agentkit.Session) {
	for ev := range sess.Subscribe() {
		switch e := ev.(type) {
		case agentkit.Token:
			c.send(ChatToken{Type: "chat.token", Frame: e.Frame, Text: e.Text})
		case agentkit.Thinking:
			c.send(ChatThinking{Type: "chat.thinking", Frame: e.Frame, Text: e.Text})
		case agentkit.ToolStart:
			c.send(ChatTool{Type: "chat.tool", Phase: "start", Frame: e.Frame, ID: e.ID, Tool: e.Tool, Args: e.Args})
		case agentkit.ToolEnd:
			c.send(ChatTool{Type: "chat.tool", Phase: "end", Frame: e.Frame, ID: e.ID, Tool: e.Tool, Args: e.Args, Result: e.Result, Err: errText(e.Err)})
		case agentkit.TurnEnd:
			c.send(ChatTurnEnd{Type: "chat.turnEnd", Frame: e.Frame, Err: errText(e.Err), Tokens: e.Tokens.Total})
		}
	}
}
