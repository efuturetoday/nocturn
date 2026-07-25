package hitl

import (
	"context"
	"log/slog"
)

// Pusher wakes a device out of band when no connection is attached to take an approval — the piece
// that makes approval truly out-of-band (a phone push). The real APNs/FCM transport is a later
// slice; this is the seam it plugs into.
type Pusher interface {
	Push(ctx context.Context, intent string) error
}

// LogPusher is the placeholder Pusher: it only logs that it would wake a device. It reserves the
// out-of-band path so the Broker's routing is complete; wiring a real transport is swapping this out.
type LogPusher struct {
	log *slog.Logger
}

// NewLogPusher builds the placeholder Pusher.
func NewLogPusher(log *slog.Logger) *LogPusher { return &LogPusher{log: log} }

// Push logs that a device would be woken; the placeholder delivers nothing.
func (p *LogPusher) Push(_ context.Context, intent string) error {
	p.log.Info("hitl push (placeholder — not delivered)", "intent", intent)
	return nil
}

var _ Pusher = (*LogPusher)(nil)
