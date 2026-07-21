package serve

import (
	"context"
	"encoding/json"
)

// ── server → client ──────────────────────────────────────────────────────────

// ApprovalRequest presents a pending out-of-band approval: an intent to render and choice labels
// (answer with the chosen index via approval.resolve, or -1 to deny).
type ApprovalRequest struct {
	Type    string   `json:"type"`
	ID      string   `json:"id"`
	Intent  string   `json:"intent"`
	Options []string `json:"options"`
}

// ApprovalResolved tells the client a pending approval is concluded — answered (here or on another
// device), timed out, or no longer needed — so it clears the prompt.
type ApprovalResolved struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ── client → server ──────────────────────────────────────────────────────────

// ApprovalResolve answers a pending approval: the chosen option index, or -1 to deny.
type ApprovalResolve struct {
	Cmd    string `json:"cmd"`
	ID     string `json:"id"`
	Choice int    `json:"choice"`
}

// approval dispatches an approval.* action.
func (c *conn) approval(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "approval.resolve":
		var m ApprovalResolve
		if err := json.Unmarshal(data, &m); err != nil {
			c.send(ctx, newError("bad approval.resolve"))
			return
		}
		c.broker.Resolve(m.ID, m.Choice)
	default:
		c.send(ctx, newError("unknown action: "+cmd))
	}
}

// Approval implements hitl.Sink: it forwards a pending approval to this client. The broker supplies
// the ctx (the connection's or the asking turn's), so none is stored on the conn.
func (c *conn) Approval(ctx context.Context, id, intent string, options []string) {
	c.send(ctx, ApprovalRequest{Type: "approval.request", ID: id, Intent: intent, Options: options})
}

// Resolved implements hitl.Sink: it tells this client to clear a concluded approval.
func (c *conn) Resolved(ctx context.Context, id string) {
	c.send(ctx, ApprovalResolved{Type: "approval.resolved", ID: id})
}
