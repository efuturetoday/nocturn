package tui

import (
	"fmt"
	"strings"
	"testing"

	gotui "github.com/grindlemire/go-tui"
)

// The rule the whole redesign turns on: a diagnostic is never cut. It is folded, and every rune that
// went in comes out.
func TestWrapLosesNothing(t *testing.T) {
	const reason = `Post "https://mcp.github.internal.acme.dev/sse": dial tcp 10.14.7.22:443: ` +
		`connect: connection refused`

	// Compared with the spaces taken out of both sides: a line break replaces a space where wrap
	// folded between words, and replaces nothing where it had to break inside one. What has to hold
	// either way is that no CHARACTER was dropped.
	bare := func(s string) string { return strings.ReplaceAll(s, " ", "") }

	for _, width := range []int{20, 40, 80, 200} {
		lines := wrap(reason, width)
		if got := bare(strings.Join(lines, "")); got != bare(reason) {
			t.Errorf("width %d: wrap lost or changed text\n got %q\nwant %q", width, got, bare(reason))
		}
		for i, line := range lines {
			if n := len([]rune(line)); n > width {
				t.Errorf("width %d: line %d is %d runes wide", width, i, n)
			}
		}
	}
}

// A URL has no spaces in it. A word wider than the line is broken across lines rather than left to
// push the box sideways.
func TestWrapBreaksAWordTooLongForTheLine(t *testing.T) {
	long := strings.Repeat("x", 50)

	lines := wrap(long, 20)

	if len(lines) != 3 {
		t.Fatalf("wrap produced %d lines for 50 runes at width 20, want 3", len(lines))
	}
	if got := strings.Join(lines, ""); got != long {
		t.Errorf("the word came back changed: %q", got)
	}
}

func TestWrapOnNothingIsNothing(t *testing.T) {
	if got := wrap("   ", 40); got != nil {
		t.Errorf("wrap of blank text = %v, want nothing to draw", got)
	}
	if got := wrap("text", 0); got != nil {
		t.Errorf("wrap at width 0 = %v, want nothing", got)
	}
}

// The board bounds a reason and says where the rest is; the section's own page has no cap. The
// constant existed and was never applied, so a stack trace would have taken the whole board.
func TestABoardReasonIsCappedAndPointsAtTheRest(t *testing.T) {
	long := wrap(strings.Repeat("reason ", 200), 40)
	if len(long) <= problemsCap {
		t.Fatalf("the fixture wraps to %d lines, want more than the cap of %d", len(long), problemsCap)
	}

	got := capReason(long, sectionMCP)

	if len(got) != problemsCap {
		t.Errorf("capReason returned %d lines, want %d", len(got), problemsCap)
	}
	last := got[problemsCap-1]
	if !strings.Contains(last, "…") || !strings.Contains(last, fmt.Sprint(sectionMCP)) {
		t.Errorf("the last line reads %q, want it to say it stops and where the rest is", last)
	}
}

// A reason that fits is left exactly as it is — no ellipsis on a complete sentence.
func TestAShortReasonIsUntouched(t *testing.T) {
	short := wrap("connection refused", 40)

	if got := capReason(short, sectionMCP); len(got) != len(short) || got[0] != short[0] {
		t.Errorf("capReason changed a reason that already fits: %v", got)
	}
}

// Tools are described by shape, not listed. The families answer "can it reach the network" without
// anyone reading forty-seven identifiers.
func TestToolFamiliesAreGroupedBiggestFirst(t *testing.T) {
	tools := []string{
		"file_read", "file_write", "file_list", "file_stat",
		"http_read", "http_write",
		"dns_resolve",
		"whoami",
	}

	lines := familyLines(tools, 2)

	if len(lines) == 0 {
		t.Fatal("familyLines produced nothing")
	}
	if !strings.HasPrefix(lines[0], "file 4") {
		t.Errorf("the first family is %q, want the biggest one first", lines[0])
	}
	if !strings.Contains(lines[0], "http 2") {
		t.Errorf("lines[0] = %q, want the second family on the same line", lines[0])
	}
	// A name with no underscore is its own family rather than being dropped.
	if !strings.Contains(strings.Join(lines, " "), "whoami 1") {
		t.Errorf("lines = %v, want a tool with no prefix to still be counted", lines)
	}
}

// A cap is never silent. A list that stops without saying so reads as a list that is complete.
func TestToolFamiliesSayWhatTheyLeftOut(t *testing.T) {
	tools := make([]string, 0, 40)
	for i := range 40 {
		tools = append(tools, fmt.Sprintf("fam%02d_call", i))
	}

	lines := familyLines(tools, 1)

	if len(lines) != 1 {
		t.Fatalf("familyLines returned %d lines for a cap of 1", len(lines))
	}
	if !strings.Contains(lines[0], "more") {
		t.Errorf("lines[0] = %q, want it to say how many families it could not show", lines[0])
	}
}

// The knowledge base is summarised by top-level directory, and that summary stays the same SHAPE at
// three files and at three thousand — which is the whole reason it replaced a prefix of the paths.
func TestDocumentsAreSummarisedByTopLevelDirectory(t *testing.T) {
	small := []string{"specs/api/auth.md", "specs/ui/keyboard.md", "rfcs/0001.md"}
	large := make([]string, 0, 3000)
	for i := range 3000 {
		large = append(large, fmt.Sprintf("specs/api/file%04d.md", i))
	}

	got := docDirLines(small, 2)
	if len(got) != 1 {
		t.Fatalf("docDirLines returned %d lines for three files, want one", len(got))
	}
	if !strings.Contains(got[0].Note, "specs/ 2") || !strings.Contains(got[0].Note, "rfcs/ 1") {
		t.Errorf("the breakdown is %q, want a count per top-level directory", got[0].Note)
	}

	if n := len(docDirLines(large, 2)); n > 2 {
		t.Errorf("three thousand files produced %d lines; the summary must not grow with the corpus", n)
	}
}

// Only the TOP level. Deeper is the page's business — a breakdown that splits by full directory is
// as long as the corpus.
func TestDocumentBreakdownStopsAtTheTopLevel(t *testing.T) {
	got := docDirLines([]string{"specs/a/b/c.md", "specs/x/y/z.md"}, 2)

	if len(got) != 1 || !strings.Contains(got[0].Note, "specs/ 2") {
		t.Errorf("docDirLines = %+v, want both files counted under one top-level directory", got)
	}
}

// The gauge is the fact the old view never showed: the catalog ceiling is ENFORCED, and notes past
// it are dropped from every prompt.
func TestGaugeFillsProportionallyAndNeverOverflows(t *testing.T) {
	for name, tc := range map[string]struct {
		used, budget, width, wantFull int
	}{
		"empty":     {0, 2048, 10, 0},
		"half":      {1024, 2048, 10, 5},
		"full":      {2048, 2048, 10, 10},
		"over":      {9999, 2048, 10, 10},
		"no budget": {10, 0, 10, 0},
	} {
		got := gauge(tc.used, tc.budget, tc.width)
		if tc.budget <= 0 {
			if got != "" {
				t.Errorf("%s: gauge = %q, want nothing to draw", name, got)
			}
			continue
		}
		if n := len([]rune(got)); n != tc.width {
			t.Errorf("%s: gauge is %d runes, want exactly %d", name, n, tc.width)
		}
		if full := strings.Count(got, "█"); full != tc.wantFull {
			t.Errorf("%s: gauge = %q with %d filled, want %d", name, got, full, tc.wantFull)
		}
	}
}

// Every section is reachable by its own digit, from the board and from any other page.
func TestEverySectionHasADigit(t *testing.T) {
	a := newTestApp(t)
	a.inspectOpen.Set(true)

	for id := sectionAgents; id < sectionCount; id++ {
		a.openSection(sectionBoard)
		digit := rune('0' + id)

		press(t, a.KeyMap(), digit)

		if got := a.inspectSection.Get(); got != id {
			t.Errorf("%q opened section %d, want %d (%s)", string(digit), got, id, id.Title())
		}
	}
}

// Escape peels one layer at a time: a page goes back to the board, the board closes. Opening the
// wrong section is the ordinary mistake and must cost one key.
func TestEscapePeelsTheWorkspaceView(t *testing.T) {
	a := newTestApp(t)
	a.inspectOpen.Set(true)
	a.openSection(sectionMCP)

	a.escapeInspect()

	if !a.inspectOpen.Get() {
		t.Fatal("Escape from a page closed the view, want it back on the board")
	}
	if got := a.inspectSection.Get(); got != sectionBoard {
		t.Errorf("the view is on section %d, want the board", got)
	}

	a.escapeInspect()

	if a.inspectOpen.Get() {
		t.Error("a second Escape did not close the view")
	}
}

// While the filter is being typed it owns the keyboard, digits included — a page narrowed to "2fa"
// must not jump to section 2 halfway through the word.
func TestFilterTypingTakesTheDigits(t *testing.T) {
	a := newTestApp(t)
	a.inspectOpen.Set(true)
	a.openSection(sectionTools)
	a.inspectTyping.Set(true)

	press(t, a.KeyMap(), '2')

	if got := a.inspectSection.Get(); got != sectionTools {
		t.Errorf("typing a digit moved to section %d; the filter should have taken it", got)
	}
	if got := a.inspectFilter.Get(); got != "2" {
		t.Errorf("the filter is %q, want the digit typed into it", got)
	}
}

// Escape while filtering clears the narrowing and stays put; it does not also navigate.
func TestEscapeWhileFilteringOnlyClearsTheFilter(t *testing.T) {
	a := newTestApp(t)
	a.inspectOpen.Set(true)
	a.openSection(sectionKnowledge)
	a.inspectTyping.Set(true)
	a.typeFilter('s', false)

	pressKey(t, a.KeyMap(), gotui.KeyEscape)

	if got := a.inspectFilter.Get(); got != "" {
		t.Errorf("the filter is %q, want it cleared", got)
	}
	if got := a.inspectSection.Get(); got != sectionKnowledge {
		t.Errorf("the view moved to section %d, want it left where it was", got)
	}
	if a.inspectTyping.Get() {
		t.Error("Escape left the keyboard in the filter")
	}
}

// Only the three unbounded lists offer a filter, and the hint line must promise exactly those.
func TestOnlyLongPagesOfferAFilter(t *testing.T) {
	for id := sectionBoard; id < sectionCount; id++ {
		a := newTestApp(t)
		a.inspectOpen.Set(true)
		a.openSection(id)

		_, offered := bindingFor(a.KeyMap(), '/', 0)
		if offered != id.Filterable() {
			t.Errorf("section %d (%s) offers / = %v, want %v", id, id.Title(), offered, id.Filterable())
		}
		if named := strings.Contains(a.hintLine(), "/ filter"); named != id.Filterable() {
			t.Errorf("section %d hint line = %q, promises a filter = %v, want %v",
				id, a.hintLine(), named, id.Filterable())
		}
	}
}

// Switching page drops the filter with it. Carried across, it would hide most of a list the reader
// never narrowed.
func TestOpeningASectionClearsTheFilter(t *testing.T) {
	a := newTestApp(t)
	a.inspectOpen.Set(true)
	a.openSection(sectionTools)
	a.typeFilter('f', false)

	a.openSection(sectionKnowledge)

	if got := a.inspectFilter.Get(); got != "" {
		t.Errorf("the filter is %q after changing page, want it cleared", got)
	}
	if got := a.inspectScroll.Get(); got != 0 {
		t.Errorf("the new page opened scrolled to %d, want the top", got)
	}
}

// The verdict counts the two kinds apart: a failure is something broken, an errand is something a
// person can go and do, and the box says which it is holding.
func TestProblemSummaryNamesBothKinds(t *testing.T) {
	if got := plural(1, "problem"); got != "1 problem" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(2, "errand"); got != "2 errands" {
		t.Errorf("plural(2) = %q", got)
	}
}

func TestBytesShort(t *testing.T) {
	for in, want := range map[int]string{0: "0B", 512: "512B", 1024: "1.0K", 1946: "1.9K"} {
		if got := bytesShort(in); got != want {
			t.Errorf("bytesShort(%d) = %q, want %q", in, got, want)
		}
	}
}
