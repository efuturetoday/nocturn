package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lmittmann/tint"

	"github.com/efuturetoday/nocturn/internal/tools"
)

// newLogger builds the DIAGNOSTIC logger — human-readable operator output answering "what is it
// doing / why did X fail". On a TTY it writes level-tinted, human-scannable lines (tint) with the
// subsystem as a [component] badge before the message; off a TTY (piped to a file / journald) it
// writes machine-parseable JSON with component as a structured field. Every line is enriched from
// ctx with the active chat id, so agentkit's own turn/tool/llm lines correlate without each call
// site passing it. Redaction is by convention: log identifiers, never secret values.
//
// def is the level to use when NOCTURN_LOG says nothing. NOCTURN_LOG always wins.
//
// also receives every record w's handler does, after ctx enrichment. The terminal UI passes its
// in-memory ring there: it owns the screen, so its diagnostics arrive as data for a pane instead of
// as text nobody can place. Nothing about the file or the level changes for the daemon.
func newLogger(w io.Writer, def slog.Level, also ...slog.Handler) *slog.Logger {
	level := def
	switch strings.ToLower(os.Getenv("NOCTURN_LOG")) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	badge := isTerminal(w)
	var base slog.Handler
	if badge {
		base = tint.NewTextHandler(w, &tint.Options{Level: level, TimeFormat: time.TimeOnly})
	} else {
		base = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	}
	if len(also) > 0 {
		base = multiHandler{gate: base, extra: also}
	}
	return slog.New(ctxHandler{Handler: base, badge: badge})
}

// multiHandler fans one record out to several handlers. slog has no built-in fan-out and this is
// the whole of it; a dependency for these few lines would not earn its place.
//
// gate alone decides what is enabled, so every destination sees exactly the same records: the
// terminal UI's log pane is a window on the file, not a second, wider log with its own formatting
// cost on lines the file drops.
type multiHandler struct {
	gate  slog.Handler
	extra []slog.Handler
}

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return m.gate.Enabled(ctx, l)
}

// Handle delivers to every handler even if one fails, and reports the first failure — a broken log
// file must not cost the pane its line.
func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	err := m.gate.Handle(ctx, r.Clone())
	for _, h := range m.extra {
		if e := h.Handle(ctx, r.Clone()); e != nil && err == nil {
			err = e
		}
	}
	return err
}

func (m multiHandler) WithAttrs(as []slog.Attr) slog.Handler {
	out := multiHandler{gate: m.gate.WithAttrs(as), extra: make([]slog.Handler, len(m.extra))}
	for i, h := range m.extra {
		out.extra[i] = h.WithAttrs(as)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := multiHandler{gate: m.gate.WithGroup(name), extra: make([]slog.Handler, len(m.extra))}
	for i, h := range m.extra {
		out.extra[i] = h.WithGroup(name)
	}
	return out
}

// logFile opens the terminal UI's diagnostic log, append-only and owner-only. One generation is
// rotated away past sizeLimit so an unattended machine cannot fill its disk with a chat log.
func logFile(root string) (*os.File, error) {
	const sizeLimit = 8 << 20
	path := filepath.Join(root, "nocturn.log")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > sizeLimit {
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// isTerminal reports whether w is a character device (a real terminal), so we colorize only when a
// human is watching. Stdlib-only (no x/term dependency): pipes and regular files are not char devices.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isTerminalFile(f)
}

// isTerminalFile is the same test for a file the caller already holds — stdin has no io.Writer side,
// and the full-screen UI needs both ends to be a terminal before it takes one over.
func isTerminalFile(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ctxHandler folds two cross-cutting concerns into every record without each call site repeating
// them: the request-scoped chat id carried on ctx (stamped by the chat manager), and the subsystem
// tag. On the human (TTY) path it lifts "component" out of the attributes into a leading [badge] on
// the message so lines scan by subsystem ([turn] [llm] [tool] [chat] …); on the JSON path it leaves
// component as a structured field for machine filtering. The logger itself is NOT carried on ctx
// (that stays a plain dependency); only these attributes are.
//
// component arrives via With("component", x) — slog bakes it into this handler's preformatted attrs,
// so WithAttrs intercepts it there (a downstream wrapper could never see it on the record). badge
// mode captures it into the field; JSON mode passes it straight through.
type ctxHandler struct {
	slog.Handler
	badge     bool
	component string
}

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	id := tools.ChatID(ctx)
	prefix := h.badge && h.component != ""
	if id != "" || prefix {
		r = r.Clone()
		if id != "" {
			r.AddAttrs(slog.String("chat", id))
		}
		if prefix {
			r.Message = "[" + h.component + "] " + r.Message
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h ctxHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if !h.badge {
		return ctxHandler{Handler: h.Handler.WithAttrs(as), badge: h.badge, component: h.component}
	}
	// TTY: siphon component out of the attrs into the badge field; pass the rest through.
	component := h.component
	var kept []slog.Attr
	for _, a := range as {
		if a.Key == "component" {
			component = a.Value.String()
			continue
		}
		kept = append(kept, a)
	}
	return ctxHandler{Handler: h.Handler.WithAttrs(kept), badge: true, component: component}
}

func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithGroup(name), badge: h.badge, component: h.component}
}
