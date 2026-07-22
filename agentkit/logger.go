package agentkit

import (
	"context"
	"log/slog"
)

// Logger is the diagnostic logging PORT — a small leveled key/value interface the consumer plugs
// its own logger into (never a hard-typed *slog.Logger), so any implementation (slog, zap, zerolog,
// custom) wraps it trivially. kv are alternating key/value pairs (like slog). With derives a logger
// that stamps the given kv on every line (e.g. component="llm"), so a subsystem tags its own lines
// without every call site repeating the key. WithContext derives a logger enriched from ctx
// (request/trace ids) for request-scoped logging.
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
	With(kv ...any) Logger
	WithContext(ctx context.Context) Logger
}

// nopLogger discards everything.
type nopLogger struct{}

// NopLogger discards everything. It is the default when no logger is configured.
func NopLogger() Logger { return nopLogger{} }

func (nopLogger) Debug(string, ...any)               {}
func (nopLogger) Info(string, ...any)                {}
func (nopLogger) Warn(string, ...any)                {}
func (nopLogger) Error(string, ...any)               {}
func (nopLogger) With(...any) Logger                 { return nopLogger{} }
func (nopLogger) WithContext(context.Context) Logger { return nopLogger{} }

// slogLogger adapts a stdlib *slog.Logger. The bound ctx is used only to feed slog's *Context log
// methods (so a handler can pull trace attributes) — it is a deliberate logger binding via
// WithContext, not request-scoped state threaded through a call chain.
type slogLogger struct {
	l   *slog.Logger
	ctx context.Context
}

// SlogLogger adapts a stdlib *slog.Logger to the port. A nil logger yields NopLogger.
func SlogLogger(l *slog.Logger) Logger {
	if l == nil {
		return NopLogger()
	}
	return slogLogger{l: l, ctx: context.Background()}
}

func (s slogLogger) Debug(msg string, kv ...any) { s.l.DebugContext(s.ctx, msg, kv...) }
func (s slogLogger) Info(msg string, kv ...any)  { s.l.InfoContext(s.ctx, msg, kv...) }
func (s slogLogger) Warn(msg string, kv ...any)  { s.l.WarnContext(s.ctx, msg, kv...) }
func (s slogLogger) Error(msg string, kv ...any) { s.l.ErrorContext(s.ctx, msg, kv...) }

func (s slogLogger) With(kv ...any) Logger {
	if len(kv) == 0 {
		return s
	}
	return slogLogger{l: s.l.With(kv...), ctx: s.ctx}
}

func (s slogLogger) WithContext(ctx context.Context) Logger {
	if ctx == nil {
		ctx = context.Background()
	}
	return slogLogger{l: s.l, ctx: ctx}
}

var _ Logger = nopLogger{}
var _ Logger = slogLogger{}
