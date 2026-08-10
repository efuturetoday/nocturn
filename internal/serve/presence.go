package serve

import (
	"context"
	"encoding/json"
)

// PresenceSet reports whether this connection is in the foreground (client → server). While any
// connection is active, approvals route to it in-band; when none is active they go out of band (a
// push). A fresh connection is active until it says otherwise.
type PresenceSet struct {
	Cmd    string `json:"cmd"`
	Active bool   `json:"active"`
}

// presence dispatches a presence.* action.
func (c *conn) presence(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "presence.set":
		var m PresenceSet
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad presence.set")
			return
		}
		// A device that may not approve was handed no broker at all (serve.go, at accept). Its
		// foreground state decides nothing, so there is nothing to record — but SetActive is a pointer
		// method that locks, so calling it on the nil broker would panic the read loop and kill the
		// connection. Same guard as approval.go and conn.go.
		if c.broker == nil {
			return
		}
		c.broker.SetActive(ctx, c, m.Active)
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}
