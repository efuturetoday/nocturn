package tui

import (
	"testing"

	gotui "github.com/grindlemire/go-tui"
)

// listOf builds a sidebar over a fixed row model, so the cursor logic can be exercised without a
// workspace.
func listOf(rows []row) (*sidebar, *[]row) {
	picked := &[]row{}
	cursor := gotui.NewState(-1)
	s := Sidebar(List{
		Box: gotui.NewRef(), Focus: func(*gotui.Ref) {}, Cursor: cursor,
		ActiveID: gotui.NewState(""), Filter: gotui.NewState(filterChats),
		Rows:     func() []row { return rows },
		Sizes:    func() (int, int) { return len(rows), 0 },
		OnSelect: func(r row) { *picked = append(*picked, r) },
	})
	return s, picked
}

// The list is conversations only: chats and agent runs interleaved by last activity. Agents are
// deliberately not in it — they are not something you read.
var demoRows = []row{
	{Kind: rowChat, ID: "aaaa", Label: "one", When: "2m"},
	{Kind: rowRun, ID: "bbbb", Label: "nightly", Source: "nightly", When: "1h"},
	{Kind: rowChat, ID: "cccc", Label: "three", When: "3d"},
}

// The bug the whole redesign is about: j and k have to MOVE the marker. They are focus-gated
// bindings on the sidebar, so this is the level they are testable at.
func TestSidebarCursorMoves(t *testing.T) {
	s, _ := listOf(demoRows)

	s.move(1)
	if got := s.cursor.Get(); got != 0 {
		t.Fatalf("after j the cursor is at %d, want the first conversation at 0", got)
	}
	s.move(1)
	if got := s.cursor.Get(); got != 1 {
		t.Fatalf("after a second j the cursor is at %d, want 1", got)
	}
	s.move(-1)
	if got := s.cursor.Get(); got != 0 {
		t.Fatalf("after k the cursor is at %d, want 0", got)
	}
}

// A placeholder — "nothing yet" — is a row too, so the list is one flat slice; the cursor steps
// over it instead of every caller special-casing it.
func TestSidebarCursorSkipsWhatCannotBeOpened(t *testing.T) {
	s, _ := listOf([]row{{Kind: rowEmpty, Label: "nothing yet"}, {Kind: rowChat, ID: "aaaa"}})

	s.move(1)

	if got := s.cursor.Get(); got != 1 {
		t.Errorf("the cursor is at %d, want 1 — row 0 cannot be opened", got)
	}
}

// It stops at the ends rather than wrapping: a list that jumps from bottom to top loses the
// reader's place.
func TestSidebarCursorStopsAtTheEnds(t *testing.T) {
	s, _ := listOf(demoRows)
	s.cursor.Set(2)

	s.move(1)

	if got := s.cursor.Get(); got != 2 {
		t.Errorf("the cursor wrapped to %d, want it to stay at the last row", got)
	}
}

func TestSidebarEnterSelectsTheRowUnderTheCursor(t *testing.T) {
	s, picked := listOf(demoRows)
	s.cursor.Set(1)

	s.selectRow()

	if len(*picked) != 1 || (*picked)[0].ID != "bbbb" {
		t.Errorf("selected %+v, want the agent run at row 1", *picked)
	}
}

// Same rule as the panes: every sidebar key is focus-gated, so j types a j while the composer has
// the keyboard.
func TestSidebarKeysAreFocusGated(t *testing.T) {
	s, _ := listOf(demoRows)

	km := s.KeyMap()
	if len(km) == 0 {
		t.Fatal("the sidebar offers no keys")
	}
	for _, b := range km {
		if !b.Pattern.FocusRequired {
			t.Errorf("binding %+v is not focus-gated", b.Pattern)
		}
	}
}

// Firing j through the KeyMap, the way the dispatch table would, moves the marker. It starts on a
// row rather than at -1 so what is asserted is the MOVE: from nothing, j lands on the first row,
// which is a different fact and TestSidebarCursorMoves already covers it.
func TestSidebarJThroughTheKeyMapMovesTheMarker(t *testing.T) {
	s, _ := listOf(demoRows)
	s.cursor.Set(0)

	press(t, s.KeyMap(), 'j')

	if got := s.cursor.Get(); got != 1 {
		t.Errorf("the cursor is at %d after j, want 1", got)
	}
}

// An empty list, because each row is a mounted sub-component and mounting needs a real App — which
// cannot be built without a terminal. The box itself is what this asserts about.
func TestSidebarIsATabStop(t *testing.T) {
	s, _ := listOf(nil)
	el := layOut(t, s, 40, 10)

	if !el.IsTabStop() {
		t.Error("the sidebar is not in the Tab ring, so Tab can never reach it")
	}
	if !el.IsFocusable() {
		t.Error("the sidebar is not focusable")
	}
}

// laidOutList gives the sidebar real geometry and only THEN its rows. The rows have to be absent
// during layout because each one is a mounted sub-component and mounting needs a real App; the
// click arithmetic needs nothing from them but the slice, which it reads at click time.
func laidOutList(t *testing.T, rows []row) (*sidebar, *[]row, *gotui.Element) {
	t.Helper()
	s, picked := listOf(nil)
	el := layOut(t, s, 40, 10)
	s.rows = func() []row { return rows }
	return s, picked, el
}

// A click picks the row it landed on and opens it — one click, because a chat list is a list of
// things to open. A conversation is two screen lines, and clicking either of them picks it.
//
// The lines are counted the way the pane stacks them: the chips, a blank, then two lines per
// conversation. Derived from the constants rather than written out, so a change to either is caught
// here instead of leaving the test aiming between two rows.
func TestSidebarClickPicksTheRowUnderThePointer(t *testing.T) {
	lines := map[string]int{
		"its name":        headerLines + rowLines,
		"its detail line": headerLines + rowLines + 1,
	}
	for name, line := range lines {
		t.Run(name, func(t *testing.T) {
			s, picked, el := laidOutList(t, demoRows)

			s.HandleMouse(gotui.MouseEvent{
				Button: gotui.MouseLeft, Action: gotui.MousePress,
				X: 3, Y: el.ContentRect().Y + line,
			})

			if got := s.cursor.Get(); got != 1 {
				t.Errorf("the cursor is at %d, want the second conversation at 1", got)
			}
			if len(*picked) != 1 || (*picked)[0].ID != "bbbb" {
				t.Errorf("picked %+v, want the conversation that was clicked", *picked)
			}
		})
	}
}

// Clicking past the last conversation does nothing. Empty space is not a choice, and snapping the
// cursor somewhere else would move the reader without being asked.
func TestSidebarClickBelowTheListDoesNothing(t *testing.T) {
	s, picked, el := laidOutList(t, demoRows)

	s.HandleMouse(gotui.MouseEvent{
		Button: gotui.MouseLeft, Action: gotui.MousePress,
		// The first line past the last conversation, counted the way the pane stacks them rather
		// than guessed: the header, then two lines per row.
		X: 3, Y: el.ContentRect().Y + headerLines + len(demoRows)*rowLines,
	})

	if len(*picked) != 0 {
		t.Errorf("picked %+v, want nothing", *picked)
	}
	if got := s.cursor.Get(); got != -1 {
		t.Errorf("the cursor moved to %d, want it left alone at -1", got)
	}
}

// The filter chips take clicks. Each is measured from the same labels the row is drawn from, so the
// target a pointer hits is the chip an eye sees.
func TestSidebarClickPicksTheFilterChip(t *testing.T) {
	// Measured off the chips themselves rather than off numbers written here, so renaming one or
	// changing the pane's width cannot leave this test aiming at the gaps between them.
	for name, tc := range map[string]struct {
		dx   int
		want listFilter
	}{
		"the chats chip":  {1, filterChats},
		"the agents chip": {chipWidth + 1, filterRuns},
	} {
		t.Run(name, func(t *testing.T) {
			s, picked, el := laidOutList(t, demoRows)
			// Start on the other one, so every case is a real move.
			s.filter.Set(filterRuns)
			if tc.want == filterRuns {
				s.filter.Set(filterChats)
			}

			s.HandleMouse(gotui.MouseEvent{
				Button: gotui.MouseLeft, Action: gotui.MousePress,
				X: el.ContentRect().X + tc.dx, Y: el.ContentRect().Y,
			})

			if got := s.filter.Get(); got != tc.want {
				t.Errorf("the filter is %q, want %q", got, tc.want)
			}
			if len(*picked) != 0 {
				t.Errorf("a click on the header opened %+v, want nothing", *picked)
			}
		})
	}
}

// The bug this replaces: the row index was (line - headerLines) / rowLines, and Go truncates integer
// division toward zero — so the header's line 0 gave -1/2 = 0 and clicking the chips opened the
// first conversation.
func TestSidebarHeaderClickNeverOpensAConversation(t *testing.T) {
	s, picked, el := laidOutList(t, demoRows)

	s.HandleMouse(gotui.MouseEvent{
		Button: gotui.MouseLeft, Action: gotui.MousePress,
		X: el.ContentRect().X + 40, Y: el.ContentRect().Y, // past the last chip
	})

	if len(*picked) != 0 {
		t.Errorf("a click on the header opened %+v, want nothing", *picked)
	}
	if got := s.cursor.Get(); got != -1 {
		t.Errorf("the cursor moved to %d, want it left alone at -1", got)
	}
}

func TestSidebarClickAsksForFocus(t *testing.T) {
	var asked *gotui.Ref
	box := gotui.NewRef()
	s := Sidebar(List{
		Box: box, Focus: func(r *gotui.Ref) { asked = r }, Cursor: gotui.NewState(-1),
		ActiveID: gotui.NewState(""), Filter: gotui.NewState(filterChats),
		Rows:     func() []row { return nil },
		Sizes:    func() (int, int) { return 0, 0 },
		OnSelect: func(row) {},
	})
	layOut(t, s, 40, 10)

	if !s.HandleMouse(gotui.MouseEvent{Button: gotui.MouseLeft, Action: gotui.MousePress, X: 3, Y: 2}) {
		t.Fatal("a click inside the list was not consumed")
	}
	if asked != box {
		t.Error("the click did not ask for the keyboard")
	}
}

func TestSidebarIgnoresTheWheelElsewhere(t *testing.T) {
	s, _, _ := laidOutList(t, demoRows)

	if s.HandleMouse(gotui.MouseEvent{Button: gotui.MouseWheelUp, Action: gotui.MousePress, X: 3, Y: 99}) {
		t.Error("a notch far below the list was consumed")
	}
}
