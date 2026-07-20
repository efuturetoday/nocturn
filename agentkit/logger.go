package agentkit

import (
	"context"
	"log/slog"
)

// Logger is the diagnostic logging PORT — a small leveled key/value interface the consumer
// plugs its own logger into (never a hard-typed *slog.Logger), so any implementation wraps
// it trivially. kv are alternating key/value pairs (like slog). WithContext derives a logger
// enriched from ctx (request/trace ids) for request-scoped logging.
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
	WithContext(ctx context.Context) Logger
}

// NopLogger discards everything. It is the default when no logger is configured.
func NopLogger() Logger { panic("TODO") }

// SlogLogger adapts a stdlib *slog.Logger to the port.
func SlogLogger(l *slog.Logger) Logger { panic("TODO") }
