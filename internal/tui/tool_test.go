package tui

import (
	"strings"
	"testing"

	gotui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/internal/tui/transcript"
)

// The call's id alone is not a key. It comes from a counter carried in the SESSION's context
// (agentkit's ToolSet.Call), so it starts over whenever a fresh session opens — after ten idle
// minutes, or at every restart. Replaying a chat that spans two sessions hands out the same id
// twice, and keyed by it alone a click on one call would open another from an earlier turn.
func TestTwoTurnsMayShareACallID(t *testing.T) {
	if toolKey(0, 1) == toolKey(3, 1) {
		t.Error("toolKey ignores the turn; two calls could share one click target")
	}
	if toolKey(0, 1) == toolKey(0, 2) {
		t.Error("toolKey ignores the call")
	}
}

// Switching conversation drops the refs. Keeping them would leave the map growing for the life of
// the process and let a click match an element from a transcript nobody is looking at any more.
func TestForgettingToolsClearsTheRefs(t *testing.T) {
	a := newTestApp(t)
	a.toolRefs.Put(toolKey(0, 7), gotui.New())

	a.forgetTools()

	if n := a.toolRefs.Len(); n != 0 {
		t.Errorf("%d refs survived the switch, want none", n)
	}
}

// The bug that made a tool call unclickable, pinned against the framework itself.
//
// A child's rect is measured from its scroll container's content box, starting at zero, while a
// mouse event is in screen coordinates. ContainsPoint converts by adding every scrollable ancestor's
// scroll offset and stops there — it never subtracts where that content box begins. So for a line
// inside a bordered, padded, scrolled pane the answer is off by the border, every time.
//
// This renders a real pane with real children and asserts what is DRAWN matches what is HIT. If
// go-tui ever fixes its own conversion, this fails and the correction in toolAt comes out.
func TestHitTestingAChildOfAScrolledPane(t *testing.T) {
	const w, h = 40, 6
	pane := gotui.New(
		gotui.WithDisplay(gotui.DisplayFlex), gotui.WithDirection(gotui.Column),
		gotui.WithScrollable(gotui.ScrollVertical),
		gotui.WithBorder(gotui.BorderSingle),
		gotui.WithSize(w, h),
	)
	kids := make([]*gotui.Element, 12)
	for i := range kids {
		kids[i] = gotui.New(gotui.WithText("line"), gotui.WithFlexShrink(0))
		pane.AddChild(kids[i])
	}

	for _, scroll := range []int{0, 4} {
		gotui.WithScrollOffset(0, scroll)(pane)
		pane.Render(gotui.NewBuffer(w, h), w, h)
		content := pane.ContentRect()

		// The child drawn on the pane's first content row is the one at the scroll offset.
		want := scroll
		screenY := content.Y

		if kids[want].ContainsPoint(content.X, screenY) {
			t.Errorf("scroll %d: ContainsPoint took a raw screen point; the correction in toolAt is no longer needed",
				scroll)
		}
		// The same point moved into the pane's CONTENT space, which is what toolAt does.
		if !kids[want].ContainsPoint(0, screenY-content.Y) {
			t.Errorf("scroll %d: the child drawn on the first content row was not found at it", scroll)
		}
	}
}

// Nothing is hit while the transcript pane has not been laid out — which is also the guard that
// stops a line scrolled out of sight, whose element still has coordinates, from being clicked.
func TestNoToolIsHitOutsideTheTranscript(t *testing.T) {
	a := newTestApp(t)
	a.toolRefs.Put(toolKey(0, 7), gotui.New())

	if _, ok := a.toolAt(5, 5); ok {
		t.Error("a click landed on a tool with no transcript pane on screen")
	}
}

// The overlay ranks as its own mode, and Escape has to preempt — a trapping modal ends its KeyMap
// with a catch-all that matches everything, and the preempt pass is the only one before it.
func TestToolOverlayIsItsOwnModeAndIsEscapable(t *testing.T) {
	a := newTestApp(t)
	a.showTool(transcript.Tool{Name: "http_read"})

	if got := a.mode(); got != modeTool {
		t.Fatalf("mode = %d with the tool overlay up, want modeTool", got)
	}

	var found bool
	for _, b := range a.KeyMap() {
		if b.Pattern.Key != gotui.KeyEscape {
			continue
		}
		found = true
		if !b.Preempt || !b.Stop {
			t.Errorf("Escape has preempt=%v stop=%v; the modal's catch-all would eat it", b.Preempt, b.Stop)
		}
	}
	if !found {
		t.Error("the tool overlay offers no Escape")
	}
}

// The scroll position survives closing, so reopening the same call puts the reader back where they
// were. A different call starts at the top: a position measured in another call's output means
// nothing here.
func TestTheToolOverlayKeepsItsPlaceForTheSameCall(t *testing.T) {
	a := newTestApp(t)
	call := transcript.Tool{ID: 7, Name: "http_read"}

	a.showTool(call)
	a.toolScroll.Set(12)
	a.closeTool()
	a.showTool(call)

	if got := a.toolScroll.Get(); got != 12 {
		t.Errorf("reopening the same call scrolled to %d, want it back at 12", got)
	}

	a.showTool(transcript.Tool{ID: 8, Name: "file_read"})

	if got := a.toolScroll.Get(); got != 0 {
		t.Errorf("a different call opened at %d, want the top", got)
	}
}

// An approval outranks it: a turn is blocked on the question, and a call somebody opened to read is
// not a reason to keep it waiting.
func TestAnApprovalOutranksTheToolOverlay(t *testing.T) {
	a := newTestApp(t)
	a.showTool(transcript.Tool{Name: "http_read"})
	ask, res := pendingAsk(t, a)
	defer func() { ask.Deny(); <-res }()

	if got := a.mode(); got != modeApprove {
		t.Errorf("mode = %d, want the approval to win", got)
	}
}

// The overlay's own bar takes a click and a drag, driven from the root — the pane has the handler
// but never sees the event, because the transcript pane comes before anything inside a modal.
func TestTheToolOverlaysScrollbarWorks(t *testing.T) {
	a := newTestApp(t)
	a.showTool(transcript.Tool{Name: "http_read", Result: strings.Repeat("line\n", 60)})

	// A real pane, laid out, so the track and the overflow are real.
	kids := make([]*gotui.Element, 60)
	for i := range kids {
		kids[i] = gotui.New(gotui.WithText("line"), gotui.WithFlexShrink(0))
	}
	pane := ScrollPane(Pane{
		Box: a.toolView, Focus: func(*gotui.Ref) {}, Title: " tool ",
		Height: 10, Focused: true, Offset: a.toolScroll,
	}, kids)
	el := layOut(t, pane, 40, 10)
	track := el.ContentRect()
	_, maxY := el.MaxScroll()
	if maxY <= 0 {
		t.Fatal("the fixture does not overflow, so there is no bar to click")
	}

	for name, action := range map[string]gotui.MouseAction{
		"a click": gotui.MousePress,
		"a drag":  gotui.MouseDrag,
	} {
		t.Run(name, func(t *testing.T) {
			a.toolScroll.Set(0)

			a.HandleMouse(gotui.MouseEvent{
				Button: gotui.MouseLeft, Action: action,
				X: track.Right() - 1, Y: track.Y + track.Height - 1,
			})

			if got := a.toolScroll.Get(); got != maxY {
				t.Errorf("%s at the foot of the track scrolled to %d, want the end at %d", name, got, maxY)
			}
		})
	}
}

// While an overlay is up the root owns the pointer whole. Letting an event through would not reach
// the overlay anyway — the framework walks mouse listeners in tree order, and the transcript pane
// comes before anything inside a modal — so a click would land on the conversation behind it.
func TestAnOverlaySwallowsThePointer(t *testing.T) {
	for name, open := range map[string]func(*app){
		"the tool detail": func(a *app) { a.showTool(transcript.Tool{Name: "http_read"}) },
		"the workspace":   func(a *app) { a.inspectOpen.Set(true) },
		"the palette":     func(a *app) { a.openPalette() },
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			open(a)

			for _, me := range []gotui.MouseEvent{
				{Button: gotui.MouseLeft, Action: gotui.MousePress, X: 5, Y: 5},
				{Button: gotui.MouseWheelDown, Action: gotui.MousePress, X: 5, Y: 5},
			} {
				if !a.HandleMouse(me) {
					t.Errorf("%v reached the layout behind the overlay", me.Button)
				}
			}
		})
	}
}

// A pane marks the keyboard itself rather than taking the framework's built-in highlight, which is
// rounded and in the terminal's own cyan — a shape this UI does not use and a colour it did not
// choose. Weight carries the signal so it survives a terminal with no truecolour.
func TestAFocusedPaneIsMarkedByItsOwnBorder(t *testing.T) {
	if paneBorder(true) == paneBorder(false) {
		t.Error("a focused pane draws the same border as an idle one")
	}
	if paneBorder(false) != gotui.BorderSingle {
		t.Error("an idle pane is not square; every box in this UI is")
	}
	if paneBorderStyle(true).Fg.Equal(paneBorderStyle(false).Fg) {
		t.Error("a focused pane draws the same border colour as an idle one")
	}
	if !paneBorderStyle(true).Fg.Equal(accent) {
		t.Error("a focused pane is not in the brand's colour")
	}
}

// The detail shows both halves WHOLE. That is its entire reason to exist — the line above it is the
// summary, and a dialog that truncates too is a second summary.
func TestToolDetailShowsInputAndOutputWhole(t *testing.T) {
	args := strings.Repeat("a", 300)
	result := strings.Repeat("b", 300)

	got := sprint(t, ToolDetail(transcript.Tool{Name: "http_read", Args: args, Result: result}))

	if !strings.Contains(got, "input") || !strings.Contains(got, "output") {
		t.Errorf("ToolDetail = %q, want both halves labelled", got)
	}
	if strings.Contains(got, "…") {
		t.Errorf("ToolDetail truncated something; it is the view that must not")
	}
}

// A call still in flight has no result, and saying so beats an empty box that reads as a result of
// nothing.
func TestToolDetailSaysWhenThereIsNothingYet(t *testing.T) {
	got := sprint(t, ToolDetail(transcript.Tool{Name: "agent_research", Running: true}))

	if !strings.Contains(got, "still running") {
		t.Errorf("ToolDetail = %q, want it to say the call has not come back", got)
	}
}

// The line's budgets come from the pane, not from constants. A wider transcript has to show more —
// but only of what there is to show, which is why the fixture is long enough to be cut at both.
func TestToolLineBudgetsGrowWithTheWidth(t *testing.T) {
	call := transcript.Tool{
		Name:   "http_read",
		Args:   strings.Repeat("a", 300),
		Result: strings.Repeat("b", 300),
	}

	narrowArgs, narrowResult := toolBudget(call, 60)
	wideArgs, wideResult := toolBudget(call, 160)

	if wideArgs <= narrowArgs || wideResult <= narrowResult {
		t.Errorf("width 160 gives %d/%d columns and width 60 gives %d/%d; the budget ignores the pane",
			wideArgs, wideResult, narrowArgs, narrowResult)
	}
}

// The complaint this answers: a call with short arguments left half the row empty while its result
// was still cut off, because each side was given a fixed share whether it needed it or not.
func TestAShortInputLeavesItsRoomToTheOutput(t *testing.T) {
	const width = 120
	short := transcript.Tool{Name: "memory_read", Args: `{"path": "people/oliver.md"}`, Result: strings.Repeat("b", 300)}
	long := transcript.Tool{Name: "memory_read", Args: strings.Repeat("a", 300), Result: strings.Repeat("b", 300)}

	_, roomWithShortInput := toolBudget(short, width)
	_, roomWithLongInput := toolBudget(long, width)

	if roomWithShortInput <= roomWithLongInput {
		t.Errorf("the output gets %d columns beside a short input and %d beside a long one; the spare room is being wasted",
			roomWithShortInput, roomWithLongInput)
	}
}

// Nothing is cut at all when the row has space for both. The line is a summary because it must be
// one, not because a number in the code says so.
func TestNothingIsCutWhenItAllFits(t *testing.T) {
	call := transcript.Tool{Name: "ping", Args: `{"host":"example.com"}`, Result: "ok"}

	args, result := toolBudget(call, 120)

	if args != len([]rune(call.Args)) || result != len([]rune(call.Result)) {
		t.Errorf("budgets are %d/%d for values of %d/%d runes; both fit and neither should be trimmed",
			args, result, len([]rune(call.Args)), len([]rune(call.Result)))
	}
}

// A failed call spends its room on the reason. The result half is a reason then, and a reason cut
// short is the failure this whole change is about.
func TestAFailedCallGivesItsRoomToTheError(t *testing.T) {
	ok := transcript.Tool{Name: "http_read", Result: "200 OK"}
	bad := transcript.Tool{Name: "http_read", Err: "dial tcp: connection refused"}

	_, okRoom := toolBudget(ok, 100)
	_, badRoom := toolBudget(bad, 100)

	if badRoom <= okRoom {
		t.Errorf("an error gets %d columns and a result %d; the reason must get the larger share",
			badRoom, okRoom)
	}
}

// Never negative and never zero, however narrow the pane: an empty column reads as a call that was
// given nothing.
func TestToolBudgetsStayUsableWhenNarrow(t *testing.T) {
	args, result := toolBudget(transcript.Tool{Name: "a_very_long_tool_name", Depth: 4}, 20)

	if args < 1 || result < 1 {
		t.Errorf("budgets are %d and %d at width 20, want both usable", args, result)
	}
}
