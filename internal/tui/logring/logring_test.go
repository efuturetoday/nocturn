package logring_test

import (
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/internal/tui/logring"
)

func TestRecordsAreRenderedIntoColumns(t *testing.T) {
	ring := logring.New(8)
	log := slog.New(ring).With("component", "chat")
	log.Warn("turn ended with error", "chat", "0f3a", "err", "boom", "n", 3)

	lines := ring.Snapshot()
	if len(lines) != 1 {
		t.Fatalf("Snapshot() = %d lines, want 1", len(lines))
	}
	got := lines[0]
	if got.Component != "chat" {
		t.Errorf("Component = %q, want %q — With attrs must reach the ring", got.Component, "chat")
	}
	if got.Chat != "0f3a" {
		t.Errorf("Chat = %q, want %q", got.Chat, "0f3a")
	}
	if got.Level != slog.LevelWarn || got.Msg != "turn ended with error" {
		t.Errorf("Line = %+v, want the warn record", got)
	}
	if !strings.Contains(got.Attrs, "err=boom") || !strings.Contains(got.Attrs, "n=3") {
		t.Errorf("Attrs = %q, want the remaining pairs", got.Attrs)
	}
	if strings.Contains(got.Attrs, "component") || strings.Contains(got.Attrs, "chat=") {
		t.Errorf("Attrs = %q, want component and chat lifted into their own columns", got.Attrs)
	}
	if len(got.Time) != len("15:04:05") {
		t.Errorf("Time = %q, want a clock", got.Time)
	}
}

func TestOldestLinesAreDropped(t *testing.T) {
	ring := logring.New(3)
	log := slog.New(ring)
	for _, m := range []string{"one", "two", "three", "four", "five"} {
		log.Info(m)
	}

	lines := ring.Snapshot()
	if len(lines) != 3 {
		t.Fatalf("Snapshot() = %d lines, want 3", len(lines))
	}
	want := []string{"three", "four", "five"}
	for i, w := range want {
		if lines[i].Msg != w {
			t.Errorf("lines[%d] = %q, want %q — the ring must return oldest first", i, lines[i].Msg, w)
		}
	}
}

func TestSnapshotCopies(t *testing.T) {
	ring := logring.New(4)
	slog.New(ring).Info("first")

	lines := ring.Snapshot()
	lines[0].Msg = "tampered"

	if got := ring.Snapshot()[0].Msg; got != "first" {
		t.Errorf("Msg = %q, want %q — Snapshot must hand out a copy", got, "first")
	}
}

func TestWithAttrsSharesTheBuffer(t *testing.T) {
	ring := logring.New(8)
	slog.New(ring).With("component", "a").Info("from a")
	slog.New(ring).With("component", "b").Info("from b")

	lines := ring.Snapshot()
	if len(lines) != 2 {
		t.Fatalf("Snapshot() = %d lines, want both derived loggers to write into the one ring", len(lines))
	}
	if lines[0].Component != "a" || lines[1].Component != "b" {
		t.Errorf("components = %q, %q, want a, b", lines[0].Component, lines[1].Component)
	}
}

// TestHandleNeverBlocks is the property the UI depends on: the goroutine drawing the pane also logs,
// so a full notify channel with nobody reading must not stall a writer.
func TestHandleNeverBlocks(t *testing.T) {
	ring := logring.New(64)
	log := slog.New(ring)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				log.Info("line", "worker", i, "n", j)
			}
		})
	}
	wg.Wait() // nobody ever drains Notify(); a blocking Handle would hang here until the test times out

	if got := len(ring.Snapshot()); got != 64 {
		t.Errorf("Snapshot() = %d lines, want the ring full at 64", got)
	}
}

func TestNotifyCoalesces(t *testing.T) {
	ring := logring.New(8)
	log := slog.New(ring)
	log.Info("one")
	log.Info("two")
	log.Info("three")

	if got := len(ring.Notify()); got != 1 {
		t.Errorf("pending signals = %d, want 1 — the channel is a wakeup, not a queue", got)
	}
	<-ring.Notify()
	select {
	case <-ring.Notify():
		t.Error("a second signal was queued for the same burst")
	default:
	}
}
