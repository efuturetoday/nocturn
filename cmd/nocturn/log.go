package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lmittmann/tint"

	"github.com/efuturetoday/nocturn/internal/tools"
)

// newLogger builds the daemon's DIAGNOSTIC logger — human-readable operator output answering "what
// is the daemon doing / why did X fail". The level comes from NOCTURN_LOG (debug|info|warn|error,
// default info). On a TTY it writes level-tinted, human-scannable lines (tint) with the subsystem as
// a [component] badge before the message; off a TTY (piped to a file / journald) it writes
// machine-parseable JSON with component as a structured field. Every line is enriched from ctx with
// the active chat id, so agentkit's own turn/tool/llm lines correlate without each call site passing
// it. Redaction is by convention: log identifiers, never secret values.
func newLogger(w io.Writer) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("NOCTURN_LOG")) {
	case "debug":
		level = slog.LevelDebug
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
	return slog.New(ctxHandler{Handler: base, badge: badge})
}

// isTerminal reports whether w is a character device (a real terminal), so we colorize only when a
// human is watching. Stdlib-only (no x/term dependency): pipes and regular files are not char devices.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
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
