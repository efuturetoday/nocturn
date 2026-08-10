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
		// Nil-tolerant on the receiver: a device that may not approve was handed no broker at all
		// (serve.go, at accept), and its foreground state decides nothing, so there is nothing to
		// record. See hitl.Broker — this used to be a guard here, and the day it was missing a single
		// presence.set from the least-trusted class panicked the read loop.
		c.broker.SetActive(ctx, c, m.Active)
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}
