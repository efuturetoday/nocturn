package agentkit

import (
	"context"
	"sync"
)

// Level ranks a Diagnostic.
type Level int

const (
	Info Level = iota
	Warn
	Error
)

func (l Level) String() string { panic("TODO") }

// Diagnostic is one advisory finding fed into a Diagnostics collector from somewhere in the
// pipeline — a spec check, a skill loader, a runtime budget/quality hit.
type Diagnostic struct {
	Level   Level
	Subject string // what it's about: "tool:http.read", "skill:pdf", "runtime"
	Message string
}

func (d Diagnostic) String() string { panic("TODO") }

// Diagnostics is a COLLECTOR — a plain container that different corners of the pipeline feed
// findings into (tool/skill validation, loaders, runtime checks). It produces nothing itself; it
// only gathers. The consumer drains it (All) or checks it (HasErrors) and logs as it sees fit.
// It is safe for concurrent feeders (parallel tool calls).
type Diagnostics struct {
	mu    sync.Mutex
	items []Diagnostic
}

// Add / Info / Warn / Error feed one finding in (the entry points for a corner holding the
// collector directly).
func (d *Diagnostics) Add(level Level, subject, msg string) { panic("TODO") }
func (d *Diagnostics) Info(subject, msg string)             { panic("TODO") }
func (d *Diagnostics) Warn(subject, msg string)             { panic("TODO") }
func (d *Diagnostics) Error(subject, msg string)            { panic("TODO") }

// All returns a snapshot of the collected findings.
func (d *Diagnostics) All() []Diagnostic { panic("TODO") }

// HasErrors reports whether any finding is Error level.
func (d *Diagnostics) HasErrors() bool { panic("TODO") }

// Len is the number of findings collected.
func (d *Diagnostics) Len() int { panic("TODO") }

type diagKey struct{}

// WithDiagnostics attaches a collector to ctx so any corner with the ctx can feed it via Diagnose
// — the same shape as the event sink.
func WithDiagnostics(ctx context.Context, d *Diagnostics) context.Context { panic("TODO") }

// DiagnosticsFrom returns the collector attached to ctx, or nil.
func DiagnosticsFrom(ctx context.Context) *Diagnostics { panic("TODO") }

// Diagnose feeds one finding into the ctx collector; no-op if none is attached (fail-open, like
// Emit). This is the feed-from-anywhere entry point.
func Diagnose(ctx context.Context, level Level, subject, msg string) { /* TODO */ }
