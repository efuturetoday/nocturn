package appserver

import "sync/atomic"

// Presence counts the companion-app connections that are currently FOREGROUND-active. The
// approval router reads it to choose the channel: a foreground app is reachable over the
// WebSocket (an open chat shows the prompt, any other chat gets an approvalPending badge), so
// no phone push is needed; when NO connection is active, a background approval must push.
//
// It is deliberately coarse (a count, not per-workspace): a foreground app can navigate to any
// workspace's pending approval via its badge, so "is anyone looking" is the whole question.
// Safe for concurrent use.
type Presence struct {
	n atomic.Int64
}

// NewPresence builds a zeroed tracker (no active connections).
func NewPresence() *Presence { return &Presence{} }

// Active reports whether at least one connection is foreground-active right now.
func (p *Presence) Active() bool { return p.n.Load() > 0 }

func (p *Presence) enter() { p.n.Add(1) }
func (p *Presence) leave() { p.n.Add(-1) }
