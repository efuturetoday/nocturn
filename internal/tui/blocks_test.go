package tui_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	gotui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/tui"
	"github.com/efuturetoday/nocturn/internal/tui/logring"
	"github.com/efuturetoday/nocturn/internal/tui/transcript"
)

// render draws a pure component to a plain string. Pure components take values and mount nothing,
// which is what lets them be rendered with no App and no terminal.
func render(t *testing.T, v gotui.Viewable) string {
	t.Helper()
	return stripANSI(gotui.Sprint(v, gotui.WithPrintWidth(100)))
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestToolLineShowsNameAndFrozenDuration(t *testing.T) {
	got := render(t, tui.ToolLine(transcript.Tool{
		Name: "http_read", Args: `{"url":"https://example.com"}`, Duration: 340 * time.Millisecond,
	}, time.Now(), 100))

	for _, want := range []string{"http_read", "340ms", "example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("ToolLine = %q, want it to contain %q", got, want)
		}
	}
}

func TestToolLineIndentsByDepth(t *testing.T) {
	now := time.Now()
	top := render(t, tui.ToolLine(transcript.Tool{Name: "agent_research"}, now, 100))
	nested := render(t, tui.ToolLine(transcript.Tool{Name: "http_read", Depth: 2}, now, 100))

	if lead(top) >= lead(nested) {
		t.Errorf("depth 0 indent %d, depth 2 indent %d — a nested call must sit further right",
			lead(top), lead(nested))
	}
}

// lead counts the leading spaces of the first non-empty line.
func lead(s string) int {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		return len(line) - len(strings.TrimLeft(line, " "))
	}
	return 0
}

func TestToolLineShowsAnError(t *testing.T) {
	got := render(t, tui.ToolLine(transcript.Tool{
		Name: "http_read", Err: "dial tcp: connection refused",
	}, time.Now(), 100))

	if !strings.Contains(got, "connection refused") {
		t.Errorf("ToolLine = %q, want the error text", got)
	}
}

func TestToolLineTicksWhileRunning(t *testing.T) {
	now := time.Now()
	got := render(t, tui.ToolLine(transcript.Tool{
		Name: "agent_research", Running: true, Started: now.Add(-7 * time.Second),
	}, now, 100))

	if !strings.Contains(got, "7s") {
		t.Errorf("ToolLine = %q, want the elapsed time of a running call", got)
	}
}

func TestApprovalRendersTheActionAndItsOptions(t *testing.T) {
	ask := tui.NewApprover()
	done := make(chan struct{})
	go func() {
		defer close(done)
		//nolint:errcheck // the goroutine only exists to produce the pending ask
		ask.Ask(t.Context(), gate.Action{Kind: "net", Target: "api.example.com"}, gate.RecallAlways,
			[]gate.Grant{{Kind: "net", Target: "*.example.com"}})
	}()
	pending := <-ask.Asks()
	defer func() { pending.Deny(); <-done }()

	got := render(t, tui.Approval(pending, 0))

	for _, want := range []string{"net", "api.example.com", "once", "this session", "always", "*.example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("Approval = %q, want it to contain %q", got, want)
		}
	}
	if !strings.Contains(got, "wider than asked") {
		t.Error("Approval must mark a widening option as wider than what was asked")
	}
}

// A nil ask still has to render: the modal is in the tree even while closed, and a component whose
// whole body is conditional returns nil, which the framework dereferences.
func TestApprovalWithNoAskRendersEmpty(t *testing.T) {
	if got := strings.TrimSpace(render(t, tui.Approval(nil, 0))); strings.Contains(got, "approve") {
		t.Errorf("Approval(nil) = %q, want no question", got)
	}
}

// The context bar's fields are the ones that answer a question without being asked, so each has to
// be there — and the model has to be there too, on the far side, which is what the width is for.
func TestContextBarShowsEveryField(t *testing.T) {
	got := render(t, tui.ContextBar("chat 8f2a · you", "⏵ thinking 12s", 1842, "opus-5", 100))

	for _, want := range []string{"chat 8f2a", "thinking 12s", "1842 tok", "opus-5"} {
		if !strings.Contains(got, want) {
			t.Errorf("ContextBar = %q, want it to contain %q", got, want)
		}
	}
}

// A turn that has cost nothing yet says nothing about tokens. A zero is noise: it is the state the
// bar is in most of the time, and a field that is usually zero teaches the eye to skip it.
func TestContextBarHidesAnEmptyTokenCount(t *testing.T) {
	if got := render(t, tui.ContextBar("new chat", "idle", 0, "opus-5", 100)); strings.Contains(got, "tok") {
		t.Errorf("ContextBar = %q, want no token field before anything has been spent", got)
	}
}

// What stands where the composer would be, when what is open cannot be written to. It has to say
// both halves: that this is read-only, and what to press instead.
func TestReadOnlyBarSaysWhyAndWhatInstead(t *testing.T) {
	got := render(t, tui.ReadOnlyBar())

	for _, want := range []string{"read only", "Ctrl+N"} {
		if !strings.Contains(got, want) {
			t.Errorf("ReadOnlyBar = %q, want it to contain %q", got, want)
		}
	}
}

func TestLogLineShowsItsColumns(t *testing.T) {
	got := render(t, tui.LogLine(logring.Line{
		Time: "15:04:05", Level: slog.LevelWarn, Component: "chat", Chat: "0f3aa1",
		Msg: "turn ended with error", Attrs: "err=boom",
	}))

	for _, want := range []string{"15:04:05", "WAR", "[chat]", "turn ended with error", "0f3a", "err=boom"} {
		if !strings.Contains(got, want) {
			t.Errorf("LogLine = %q, want it to contain %q", got, want)
		}
	}
}

// The severity has to be COLOURED, and that is not a matter of taste: it was written as
// class={levelClass(...)} and the generator dropped it without a word, because a class attribute is
// only read when it is a string literal. Asserting on the cell style is the only way that shows.
func TestLogLineColoursTheSeverity(t *testing.T) {
	type tc struct {
		level slog.Level
		want  gotui.Color
	}
	tests := map[string]tc{
		"error is red":     {slog.LevelError, gotui.Red},
		"warning is amber": {slog.LevelWarn, gotui.Yellow},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			el := tui.LogLine(logring.Line{Time: "15:04:05", Level: tt.level, Msg: "x"}).GetRoot()
			buf := gotui.NewBuffer(60, 3)
			el.Render(buf, 60, 3)

			cell, ok := firstCellOf(buf, 60, 3, strings.ToUpper(tt.level.String()[:3]))
			if !ok {
				t.Fatalf("the severity column is missing from the rendered line")
			}
			if !cell.Style.Fg.Equal(tt.want) {
				t.Errorf("severity foreground = %v, want %v", cell.Style.Fg, tt.want)
			}
		})
	}
}

// firstCellOf returns the cell where word starts in the rendered buffer.
func firstCellOf(buf *gotui.Buffer, w, h int, word string) (gotui.Cell, bool) {
	runes := []rune(word)
	for y := range h {
		for x := 0; x <= w-len(runes); x++ {
			hit := true
			for i, r := range runes {
				if buf.Cell(x+i, y).Rune != r {
					hit = false
					break
				}
			}
			if hit {
				return buf.Cell(x, y), true
			}
		}
	}
	return gotui.Cell{}, false
}
