package main

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// newLogger builds the daemon's DIAGNOSTIC logger — human-readable operator output that answers
// "what is the daemon doing / why did X fail". It writes to w (stderr for `serve`; a file for the
// TUI, so it never corrupts the bubbletea screen). The level comes from NOCTURN_LOG
// (debug|info|warn|error), default info.
//
// This is distinct from the (future, M7) durable security AUDIT sink. Redaction is by convention:
// log identifiers, never secret values — device ids not bearers, targets not payloads.
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
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
