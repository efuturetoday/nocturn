package workspace

import (
	"context"
	"sync"

	"github.com/efuturetoday/nocturn/app/tools"
)

// notifier is the workspace's seam for every proactive message leaving it (a notify tool call, a
// reminder firing). It stamps the workspace name — which the workspace owns and no tool should carry
// — and then ROUTES the message by presence, the same either/or the approval broker uses:
//
//   - a device is in the foreground → deliver over the live connection (the observer). The device in
//     the user's hand shows it; a push would only duplicate it, and whether that duplicate is visible
//     depends on the app's foreground-presentation config, which we don't control here.
//   - no device is in the foreground → wake one out of band (the sender). A fired reminder has already
//     left the pending list, so a sleeping device would otherwise see nothing until it reconnects.
//
// This is not a fan-out to both: routing to exactly one path mirrors approvals and avoids the double
// delivery. `active` is the presence probe (nil = treat as none active, e.g. the terminal, which then
// always takes the sender path — there is its print fallback).
type notifier struct {
	ws     string
	next   tools.Notifier // out-of-band sender (push / terminal print); nil = not configured
	active func() bool    // any device in the foreground; nil = none

	mu  sync.RWMutex
	obs func(tools.Notification)
}

// observe registers the in-app delivery callback (the live-connection broadcast). Set once, at wiring
// time, before serving.
func (n *notifier) observe(fn func(tools.Notification)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.obs = fn
}

// Notify stamps the workspace, then delivers by presence: to a watching device over its live
// connection, or out of band to wake a sleeping one. The out-of-band path returns its error so a
// caller still learns when nothing reached the user; the in-app path is best-effort (a broadcast to
// whoever is connected) and reports nothing.
func (n *notifier) Notify(ctx context.Context, msg tools.Notification) error {
	msg.Ws = n.ws

	if n.anyActive() {
		n.mu.RLock()
		obs := n.obs
		n.mu.RUnlock()
		if obs != nil {
			obs(msg)
			return nil
		}
		// A device is active but no in-app path is wired (should not happen once Serve wires the
		// observer): fall through to the sender rather than drop the message.
	}

	if n.next == nil {
		return nil
	}
	return n.next.Notify(ctx, msg)
}

// anyActive reports whether a device is in the foreground; a nil probe means none is.
func (n *notifier) anyActive() bool {
	return n.active != nil && n.active()
}
