package agentkit

import (
	"context"
	"fmt"
	"sync"
)

// Level ranks a Diagnostic.
type Level int

const (
	Info Level = iota
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Info:
		return "info"
	case Warn:
		return "warn"
	case Error:
		return "error"
	default:
		return "unknown"
	}
}

// Diagnostic is one advisory finding fed into a Diagnostics collector from somewhere in the
// pipeline — a spec check, a skill loader, a runtime budget/quality hit.
type Diagnostic struct {
	Level   Level
	Subject string // what it's about: "tool:http.read", "skill:pdf", "runtime"
	Message string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("[%s] %s: %s", d.Level, d.Subject, d.Message)
}

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
func (d *Diagnostics) Add(level Level, subject, msg string) {
	d.mu.Lock()
	d.items = append(d.items, Diagnostic{Level: level, Subject: subject, Message: msg})
	d.mu.Unlock()
}

func (d *Diagnostics) Info(subject, msg string)  { d.Add(Info, subject, msg) }
func (d *Diagnostics) Warn(subject, msg string)  { d.Add(Warn, subject, msg) }
func (d *Diagnostics) Error(subject, msg string) { d.Add(Error, subject, msg) }

// All returns a snapshot copy of the collected findings.
func (d *Diagnostics) All() []Diagnostic {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Diagnostic, len(d.items))
	copy(out, d.items)
	return out
}

// HasErrors reports whether any finding is Error level.
func (d *Diagnostics) HasErrors() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, it := range d.items {
		if it.Level == Error {
			return true
		}
	}
	return false
}

// Len is the number of findings collected.
func (d *Diagnostics) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.items)
}

type diagKey struct{}

// WithDiagnostics attaches a collector to ctx so any corner with the ctx can feed it via Diagnose —
// the same shape as the event sink. A nil collector leaves ctx unchanged.
func WithDiagnostics(ctx context.Context, d *Diagnostics) context.Context {
	if d == nil {
		return ctx
	}
	return context.WithValue(ctx, diagKey{}, d)
}

// DiagnosticsFrom returns the collector attached to ctx, or nil.
func DiagnosticsFrom(ctx context.Context) *Diagnostics {
	d, _ := ctx.Value(diagKey{}).(*Diagnostics)
	return d
}

// Diagnose feeds one finding into the ctx collector; no-op if none is attached (fail-open, like
// Emit). This is the feed-from-anywhere entry point.
func Diagnose(ctx context.Context, level Level, subject, msg string) {
	if d := DiagnosticsFrom(ctx); d != nil {
		d.Add(level, subject, msg)
	}
}
