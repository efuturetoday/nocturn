// Package logring is a slog.Handler that keeps the last N records in memory for the terminal UI's
// log pane. It exists because a full-screen UI owns the screen: nothing may write to stdout or
// stderr while it runs, so the diagnostic stream has to arrive as data instead of as text. The
// records go to a file as well — this ring is only the window a keypress opens on them.
//
// Handle never blocks and never grows: the UI goroutine logs too, so a handler that waited on a
// reader would deadlock the program it is reporting on.
package logring

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// Line is one record, already rendered into the fields the pane draws. Formatting happens here,
// once per record, rather than per frame in the renderer.
type Line struct {
	Time      string // clock only; the date is in the log file
	Level     slog.Level
	Component string // the subsystem tag, shown as its own column
	Chat      string // the chat this line belongs to, when the record carries one
	Msg       string
	Attrs     string // everything else, rendered k=v
}

// buffer is the shared circular store. Every handler derived by WithAttrs points at the same one,
// so a logger built with With still writes into the one pane.
type buffer struct {
	mu     sync.Mutex
	lines  []Line
	next   int  // write cursor
	filled bool // the cursor has wrapped at least once
	notify chan struct{}
}

// Ring is a slog.Handler writing into a fixed-size buffer.
type Ring struct {
	buf *buffer
	// attrs are the attributes handed down by With; they precede a record's own.
	attrs []slog.Attr
}

var _ slog.Handler = (*Ring)(nil)

// New returns a ring holding the last n records.
func New(n int) *Ring {
	if n < 1 {
		n = 1
	}
	return &Ring{buf: &buffer{lines: make([]Line, n), notify: make(chan struct{}, 1)}}
}

// Notify returns a channel that receives once whenever the ring changes. It is a coalescing signal,
// not a queue: sends are non-blocking and a pending signal is not duplicated, so a reader that wakes
// late gets one wakeup for a burst and reads the whole burst with Snapshot.
func (r *Ring) Notify() <-chan struct{} { return r.buf.notify }

// Enabled reports true for every level: the ring adds no filter of its own. Whatever fans records
// out to it decides the level, so the pane shows exactly what the log file records.
func (r *Ring) Enabled(context.Context, slog.Level) bool { return true }

func (r *Ring) Handle(_ context.Context, rec slog.Record) error {
	line := Line{
		Time:  rec.Time.Format("15:04:05"),
		Level: rec.Level,
		Msg:   rec.Message,
	}
	var rest strings.Builder
	collect := func(a slog.Attr) bool {
		switch a.Key {
		case "component":
			line.Component = a.Value.String()
		case "chat":
			line.Chat = a.Value.String()
		default:
			if rest.Len() > 0 {
				rest.WriteByte(' ')
			}
			rest.WriteString(a.Key)
			rest.WriteByte('=')
			rest.WriteString(a.Value.String())
		}
		return true
	}
	for _, a := range r.attrs {
		collect(a)
	}
	rec.Attrs(collect)
	line.Attrs = rest.String()

	b := r.buf
	b.mu.Lock()
	b.lines[b.next] = line
	b.next = (b.next + 1) % len(b.lines)
	if b.next == 0 {
		b.filled = true
	}
	b.mu.Unlock()

	select {
	case b.notify <- struct{}{}:
	default: // a signal is already pending; the reader will see this line too
	}
	return nil
}

// WithAttrs returns a handler that prepends as to every record it writes into the same buffer.
func (r *Ring) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return r
	}
	attrs := make([]slog.Attr, 0, len(r.attrs)+len(as))
	attrs = append(attrs, r.attrs...)
	attrs = append(attrs, as...)
	return &Ring{buf: r.buf, attrs: attrs}
}

// WithGroup is a no-op: the pane renders flat k=v pairs, and nocturn's logging never groups.
func (r *Ring) WithGroup(string) slog.Handler { return r }

// Snapshot returns the buffered lines, oldest first.
func (r *Ring) Snapshot() []Line {
	b := r.buf
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.filled {
		return append([]Line{}, b.lines[:b.next]...)
	}
	out := make([]Line, 0, len(b.lines))
	out = append(out, b.lines[b.next:]...)
	return append(out, b.lines[:b.next]...)
}
