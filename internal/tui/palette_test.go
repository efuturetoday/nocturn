package tui

import (
	"strings"
	"testing"
	"time"

	gotui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/internal/chat"
)

// sprint draws a pure component to a plain string. It is the internal twin of the helper in
// blocks_test.go, and it exists because paletteEntry is unexported: the rows cannot be built from
// outside the package.
func sprint(t *testing.T, v gotui.Viewable) string {
	t.Helper()
	out := gotui.Sprint(v, gotui.WithPrintWidth(100))
	var b strings.Builder
	for i := 0; i < len(out); i++ {
		if out[i] == 0x1b {
			for i < len(out) && out[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(out[i])
	}
	return b.String()
}

var demoEntries = []paletteEntry{
	{Verb: "new chat", Detail: "start a fresh conversation"},
	{Verb: "fire agent", Detail: "daily — the morning digest"},
	{Verb: "open", Detail: "quarterly report · 8f2a"},
}

// The filter matches over the verb AND the detail together, so both "what kind of thing" and "which
// one" find the same row.
func TestPaletteFilterMatchesVerbAndDetail(t *testing.T) {
	for name, tc := range map[string]struct {
		query string
		want  int
	}{
		"an empty query keeps everything": {"", 3},
		"by verb":                         {"fire", 1},
		"by detail":                       {"daily", 1},
		"case does not matter":            {"DAILY", 1},
		"no match is no rows":             {"zzz", 0},
	} {
		if got := len(filterEntries(demoEntries, tc.query)); got != tc.want {
			t.Errorf("%s: %q matched %d entries, want %d", name, tc.query, got, tc.want)
		}
	}
}

// Typing rebuilds the list under the cursor, so the cursor goes back to the top. Left where it was,
// it would be pointing at something the reader never chose.
func TestTypingResetsThePaletteCursor(t *testing.T) {
	a := newTestApp(t)
	a.openPalette()
	a.paletteCursor.Set(2)

	a.typePalette('f', false)

	if got := a.paletteCursor.Get(); got != 0 {
		t.Errorf("the cursor is at %d after typing, want it back at 0", got)
	}
	if got := a.paletteQuery.Get(); got != "f" {
		t.Errorf("the query is %q, want %q", got, "f")
	}
}

func TestBackspaceShortensTheQuery(t *testing.T) {
	a := newTestApp(t)
	a.openPalette()
	a.paletteQuery.Set("fir")

	a.typePalette(0, true)

	if got := a.paletteQuery.Get(); got != "fi" {
		t.Errorf("the query is %q after backspace, want %q", got, "fi")
	}
}

// Backspace on an empty query is a no-op rather than a panic: it is the first key a reader presses
// when they change their mind about opening the palette at all.
func TestBackspaceOnAnEmptyQueryIsHarmless(t *testing.T) {
	a := newTestApp(t)
	a.openPalette()

	a.typePalette(0, true)

	if got := a.paletteQuery.Get(); got != "" {
		t.Errorf("the query is %q, want it still empty", got)
	}
}

// The cursor stops at the ends. Without a workspace the palette still offers its own commands, which
// is what makes this checkable at all.
func TestPaletteCursorStopsAtTheEnds(t *testing.T) {
	a := newTestApp(t)
	a.openPalette()
	n := len(a.paletteEntries())
	if n < 2 {
		t.Fatalf("the palette offers %d entries with no workspace, want at least two to walk", n)
	}

	a.movePalette(-1)
	if got := a.paletteCursor.Get(); got != 0 {
		t.Errorf("the cursor moved to %d above the first row, want 0", got)
	}
	for range n + 3 {
		a.movePalette(1)
	}
	if got := a.paletteCursor.Get(); got != n-1 {
		t.Errorf("the cursor is at %d past the last row, want %d", got, n-1)
	}
}

// Escape leaves nothing armed. A deletion that survived the dialog it was chosen in would be waiting
// to be confirmed by the next Enter the reader pressed for something else.
func TestClosingThePaletteDisarmsADeletion(t *testing.T) {
	a := newTestApp(t)
	a.openPalette()
	a.paletteArmed.Set("8f2a")
	a.paletteStep.Set(stepConfirm)

	a.closePalette()

	if got := a.paletteArmed.Get(); got != "" {
		t.Errorf("%q is still armed for deletion after the palette closed", got)
	}
	if got := a.paletteStep.Get(); got != stepRoot {
		t.Errorf("the palette reopens on step %d, want the command list", got)
	}
}

// An armed deletion is the ONLY thing on offer, and the row has to name what it will destroy — a
// confirmation that does not say what it confirms is not one.
func TestAnArmedDeletionCrowdsOutEverythingElse(t *testing.T) {
	a := newTestApp(t)
	a.openPalette()
	a.paletteArmed.Set("8f2a11")
	a.paletteStep.Set(stepConfirm)

	entries := a.paletteEntries()

	if len(entries) != 1 {
		t.Fatalf("the palette offers %d entries while a deletion is armed, want exactly one", len(entries))
	}
	if !strings.Contains(entries[0].Verb, "8f2a") {
		t.Errorf("the confirmation reads %q, want it to name what it deletes", entries[0].Verb)
	}
}

// The whole reason the palette has two steps. One step made a list whose length is commands TIMES
// conversations, with "open" and "delete" repeating down the screen — which is what a reader
// actually saw and could not read.
func TestTheCommandListNamesEachVerbOnce(t *testing.T) {
	a := newTestApp(t)
	a.openPalette()

	seen := map[string]int{}
	for _, e := range a.paletteEntries() {
		seen[e.Verb]++
	}
	for verb, n := range seen {
		if n > 1 {
			t.Errorf("the command list offers %q %d times, want once", verb, n)
		}
	}
}

// A verb that needs something to act on hands over to a picker instead of acting, and says so.
func TestVerbsThatNeedASubjectOpenAPicker(t *testing.T) {
	a := newTestApp(t)
	a.openPalette()

	entries := filterEntries(a.paletteEntries(), "open chat")
	if len(entries) != 1 {
		t.Fatalf("found %d entries for \"open chat\", want exactly one", len(entries))
	}
	if !strings.HasSuffix(entries[0].Verb, "…") {
		t.Errorf("the verb reads %q, want the ellipsis that promises a second question", entries[0].Verb)
	}

	entries[0].run()

	if got := a.paletteStep.Get(); got != stepOpen {
		t.Errorf("the palette is on step %d, want the conversation picker", got)
	}
	if !a.paletteOpen.Get() {
		t.Error("choosing a verb closed the palette; it should have asked the second question")
	}
}

// Escape backs out of a picker rather than throwing the reader out of the palette: choosing "delete"
// and meaning "open" is the ordinary case, and it must cost one key, not two and a reopen.
func TestEscapeBacksOutOfAPickerBeforeItCloses(t *testing.T) {
	a := newTestApp(t)
	a.openPalette()
	a.goToStep(stepDelete)

	a.escapePalette()

	if !a.paletteOpen.Get() {
		t.Fatal("Escape closed the palette from a picker, want it back on the command list")
	}
	if got := a.paletteStep.Get(); got != stepRoot {
		t.Errorf("the palette is on step %d, want the command list", got)
	}

	a.escapePalette()

	if a.paletteOpen.Get() {
		t.Error("a second Escape did not close the palette")
	}
}

// Switching question clears the answer to the previous one: a query typed to narrow the verbs would
// otherwise arrive at the picker and hide most of it, filtered by something no longer on screen.
func TestChangingStepClearsTheQuery(t *testing.T) {
	a := newTestApp(t)
	a.openPalette()
	a.typePalette('o', false)

	a.goToStep(stepOpen)

	if got := a.paletteQuery.Get(); got != "" {
		t.Errorf("the query is %q after moving to the picker, want it cleared", got)
	}
}

func TestPaletteRowMarksTheCursor(t *testing.T) {
	picked := sprint(t, PaletteRow(demoEntries[1], true))
	if !strings.Contains(picked, "▸") {
		t.Errorf("PaletteRow(selected) = %q, want a marker on it", picked)
	}
	if !strings.Contains(picked, "fire agent") || !strings.Contains(picked, "daily") {
		t.Errorf("PaletteRow = %q, want the verb and its detail", picked)
	}
	if plain := sprint(t, PaletteRow(demoEntries[0], false)); strings.Contains(plain, "▸") {
		t.Errorf("PaletteRow(unselected) = %q, want no marker", plain)
	}
}

// A filter that matches nothing says so, rather than showing an empty box that reads as a hang.
func TestPaletteSaysWhenNothingMatches(t *testing.T) {
	if got := sprint(t, Palette(stepRoot.title(), "zzz", nil, 0)); !strings.Contains(got, "nothing matches") {
		t.Errorf("Palette with no entries = %q, want it to say so", got)
	}
}

// Every step names itself. A picker showing six conversation names with no heading is a list with no
// verb, and Enter on it could mean read this or destroy it.
func TestEveryStepNamesItself(t *testing.T) {
	titles := map[string]bool{}
	for _, s := range []paletteStep{stepRoot, stepOpen, stepDelete, stepFire, stepConfirm} {
		title := s.title()
		if strings.TrimSpace(title) == "" {
			t.Errorf("step %d has no title", s)
		}
		if titles[title] {
			t.Errorf("step %d reuses the title %q; two questions that look the same are one question", s, title)
		}
		titles[title] = true
	}
}

// Newest activity first, whichever half is on screen — the list is scanned, and the thing scanned
// for is almost always the thing that just happened.
func TestRowsAreNewestFirst(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	chats := []chat.Meta{
		{ID: "aaaa", Name: "older", Updated: now.Add(-2 * time.Hour)},
		{ID: "cccc", Name: "newer", Updated: now.Add(-1 * time.Minute)},
	}

	got := mergeRows(chats, nil, filterChats, now)

	if len(got) != 2 {
		t.Fatalf("mergeRows returned %d rows, want both chats", len(got))
	}
	if got[0].ID != "cccc" {
		t.Errorf("the first row is %q, want the most recent activity first", got[0].ID)
	}
}

// Each half shows its own and nothing else, and a run carries the badge that says which agent had
// it — the rows are the same shape either way, so the badge is what tells them apart.
func TestEachFilterShowsItsOwnHalf(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	chats := []chat.Meta{{ID: "aaaa", Name: "a chat", Updated: now}}
	runs := []chat.Meta{{ID: "bbbb", Agent: "daily", Source: chat.SourceAgent, Updated: now}}

	for name, tc := range map[string]struct {
		filter   listFilter
		wantID   string
		wantKind rowKind
	}{
		"chats only":  {filterChats, "aaaa", rowChat},
		"agents only": {filterRuns, "bbbb", rowRun},
	} {
		got := mergeRows(chats, runs, tc.filter, now)
		if len(got) != 1 || got[0].ID != tc.wantID {
			t.Errorf("%s: mergeRows returned %+v, want only %q", name, got, tc.wantID)
			continue
		}
		if got[0].Kind != tc.wantKind {
			t.Errorf("%s: the row is %+v, want kind %d", name, got[0], tc.wantKind)
		}
		if tc.wantKind == rowRun && got[0].Source != "daily" {
			t.Errorf("%s: the run is not badged with the agent that owns it: %+v", name, got[0])
		}
	}
}

// An empty list still says something, and what it says depends on what is being hidden: an empty
// runs filter is not the same fact as an empty workspace.
func TestAnEmptyListExplainsItself(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

	got := mergeRows(nil, nil, filterRuns, now)

	if len(got) != 1 || got[0].Kind != rowEmpty {
		t.Fatalf("mergeRows returned %+v, want a single placeholder", got)
	}
	if !strings.Contains(got[0].Label, "agent runs") {
		t.Errorf("the placeholder reads %q, want it to name what is missing", got[0].Label)
	}
}
