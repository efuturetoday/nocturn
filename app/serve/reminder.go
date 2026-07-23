package serve

import (
	"context"
	"encoding/json"
	"time"

	"github.com/efuturetoday/nocturn/app/tools"
)

// ── client → server (cmd) ────────────────────────────────────────────────────

// ReminderList requests a workspace's pending reminders.
type ReminderList struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// ReminderCancel drops one pending reminder. There is deliberately no create command: reminders are
// set by the model through the remind tool, which passes the gate — a device may only view and
// cancel, never mint one behind the gate's back.
type ReminderCancel struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
	ID  string `json:"id"`
}

// ── server → client (type) ───────────────────────────────────────────────────

// ReminderInfo is one pending reminder on the wire. FireAt is RFC3339 (offset included) so a device
// in another timezone renders the daemon's intended instant, not a naive wall clock.
type ReminderInfo struct {
	ID      string `json:"id"`
	FireAt  string `json:"fireAt"`
	Message string `json:"message"`
	Title   string `json:"title,omitempty"`
}

// ReminderListResult is a workspace's pending reminders, soonest first. A fired reminder is gone —
// this is the pending set, never a history.
type ReminderListResult struct {
	Type      string         `json:"type"`
	Ws        string         `json:"ws"`
	Reminders []ReminderInfo `json:"reminders"`
}

// ReminderChanged is broadcast to every device when a workspace's pending set changes (the model set
// one, one was cancelled, one fired). It carries no payload: the client re-lists, so two devices
// racing on a cancel converge on the daemon's state rather than on their own guesses.
type ReminderChanged struct {
	Type string `json:"type"`
	Ws   string `json:"ws"`
}

// Notification is a proactive message delivered to an AWAKE device over its live connection — a
// reminder that just fired, or a notify tool call. It is the in-app half of a delivery whose other
// half is an out-of-band push: the push is suppressed or easy to miss while the app is in the
// foreground, and a fired reminder leaves the pending list immediately, so without this the most
// likely case (phone in hand) would deliver nothing. A device may therefore see both; showing the
// in-app one and letting the OS drop the duplicate is the intended behaviour.
//
// Kind is "remind" or "notify". ChatID, when set, is the chat or agent run it came from — what a tap
// should open.
type Notification struct {
	Type    string `json:"type"`
	Ws      string `json:"ws"`
	Kind    string `json:"kind"`
	ChatID  string `json:"chatId,omitempty"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message"`
}

// reminderInfos converts the workspace's reminders to their wire form.
func reminderInfos(rems []tools.Reminder) []ReminderInfo {
	out := make([]ReminderInfo, 0, len(rems))
	for _, r := range rems {
		out = append(out, ReminderInfo{
			ID:      r.ID,
			FireAt:  r.FireAt.Format(time.RFC3339),
			Message: r.Message,
			Title:   r.Title,
		})
	}
	return out
}

// reminder dispatches a reminder.* action.
func (c *conn) reminder(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "reminder.list":
		var m ReminderList
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad reminder.list")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		c.send(ctx, ReminderListResult{Type: "reminder.list", Ws: m.Ws, Reminders: reminderInfos(ws.Reminders())})
	case "reminder.cancel":
		var m ReminderCancel
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad reminder.cancel")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		// The cancel itself broadcasts reminder.changed (OnReminderChange), so every device —
		// including this one — refreshes from the daemon. No reply is needed or sent.
		ws.CancelReminder(m.ID)
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}
