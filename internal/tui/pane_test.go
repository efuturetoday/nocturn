package tui

import (
	"fmt"
	"math"
	"testing"

	gotui "github.com/grindlemire/go-tui"
)

// layOut renders a component's element tree into a buffer so the scroll geometry is real. Struct
// components take an *tui.App only to mount children; a pane mounts none, so nil is enough.
func layOut(t *testing.T, c interface {
	Render(*gotui.App) *gotui.Element
}, w, h int) *gotui.Element {
	t.Helper()
	el := c.Render(nil)
	el.Render(gotui.NewBuffer(w, h), w, h)
	return el
}

// The scrollbar takes clicks. go-tui draws one and parses drags, but consumes neither: MouseDrag
// appears only in its two input parsers, and nothing hit-tests the bar. The column and the track are
// derivable from the element, which is what these assert against.
func TestScrollbarClickJumpsToThePosition(t *testing.T) {
	p := filledPane(t, 60)
	el := layOut(t, p, 40, 10)
	track := el.ContentRect()
	_, maxY := el.MaxScroll()

	if maxY <= 0 {
		t.Fatal("the fixture does not overflow, so there is no bar to click")
	}

	p.HandleMouse(gotui.MouseEvent{
		Button: gotui.MouseLeft, Action: gotui.MousePress,
		X: track.Right() - 1, Y: track.Y + track.Height - 1,
	})

	if got := p.offset.Get(); got != maxY {
		t.Errorf("a click at the foot of the track scrolled to %d, want the end at %d", got, maxY)
	}
	if !p.sticky.Get() {
		t.Error("scrolling to the end did not resume following the tail")
	}

	p.HandleMouse(gotui.MouseEvent{
		Button: gotui.MouseLeft, Action: gotui.MousePress,
		X: track.Right() - 1, Y: track.Y,
	})

	if got := p.offset.Get(); got != 0 {
		t.Errorf("a click at the head of the track scrolled to %d, want the beginning", got)
	}
}

// A drag is the same gesture continued. It has to be taken too, or the bar can be grabbed and not
// moved — and a drag is the only motion the framework reports at all.
func TestScrollbarDragFollowsThePointer(t *testing.T) {
	p := filledPane(t, 60)
	el := layOut(t, p, 40, 10)
	track := el.ContentRect()

	p.HandleMouse(gotui.MouseEvent{
		Button: gotui.MouseLeft, Action: gotui.MouseDrag,
		X: track.Right() - 1, Y: track.Y + track.Height - 1,
	})

	if got := p.offset.Get(); got == 0 {
		t.Error("dragging the bar to the foot of the track did not move the pane")
	}
}

// A click anywhere else is still a click: it takes the keyboard and leaves the position alone.
func TestAClickBesideTheBarDoesNotScroll(t *testing.T) {
	p := filledPane(t, 60)
	el := layOut(t, p, 40, 10)
	track := el.ContentRect()
	p.offset.Set(7)
	p.sticky.Set(false)

	p.HandleMouse(gotui.MouseEvent{
		Button: gotui.MouseLeft, Action: gotui.MousePress,
		X: track.X, Y: track.Y + 2,
	})

	if got := p.offset.Get(); got != 7 {
		t.Errorf("a click inside the content scrolled to %d, want it left at 7", got)
	}
}

func filledPane(t *testing.T, lines int) *scrollPane {
	t.Helper()
	kids := make([]*gotui.Element, lines)
	for i := range kids {
		kids[i] = gotui.New(gotui.WithText(fmt.Sprintf("line %d", i)))
	}
	return ScrollPane(Pane{Box: gotui.NewRef(), Focus: func(*gotui.Ref) {}, Height: 8, Follow: true}, kids)
}

// A fresh pane follows its tail by asking for an impossible offset. The layout clamps it, which is
// what makes following cost nothing: no watcher, and no window between appending a line and
// pinning to it.
func TestScrollPaneStartsStuckToTheBottom(t *testing.T) {
	p := filledPane(t, 40)

	if got := p.scrollOffset(); got != math.MaxInt {
		t.Errorf("scrollOffset() = %d, want MaxInt while sticky", got)
	}
	el := layOut(t, p, 40, 8)
	_, at := el.ScrollOffset()
	_, maxY := el.MaxScroll()
	if maxY == 0 {
		t.Fatal("40 lines in 8 rows must overflow; the test proves nothing otherwise")
	}
	if at != maxY {
		t.Errorf("laid out at %d, want the bottom %d", at, maxY)
	}
}

// Scrolling up unsticks; scrolling back to the bottom re-sticks, so following resumes without a
// separate key.
func TestScrollPaneUnsticksAndResticks(t *testing.T) {
	p := filledPane(t, 40)
	layOut(t, p, 40, 8)

	p.scrollBy(-1)
	if p.sticky.Get() {
		t.Fatal("scrolling up left the pane stuck to the bottom")
	}
	if got := p.scrollOffset(); got == math.MaxInt {
		t.Error("an unstuck pane still asks for MaxInt")
	}

	layOut(t, p, 40, 8)
	p.scrollBy(1)
	if !p.sticky.Get() {
		t.Error("scrolling back to the bottom did not resume following")
	}
}

func TestScrollPaneTopAndEnd(t *testing.T) {
	p := filledPane(t, 40)
	layOut(t, p, 40, 8)

	p.scrollTop()
	if p.sticky.Get() || p.offset.Get() != 0 {
		t.Errorf("scrollTop: sticky=%v offset=%d, want false/0", p.sticky.Get(), p.offset.Get())
	}
	p.scrollEnd()
	if !p.sticky.Get() {
		t.Error("scrollEnd did not resume following")
	}
}

// The keys are the whole point of the pane being a component: only a component can carry
// focus-gated bindings, and the dispatch table refuses to build when one has no IsFocused to ask.
func TestScrollPaneKeysAreFocusGated(t *testing.T) {
	p := filledPane(t, 40)

	km := p.KeyMap()
	if len(km) == 0 {
		t.Fatal("the pane offers no keys")
	}
	for _, b := range km {
		if !b.Pattern.FocusRequired {
			t.Errorf("binding %+v is not focus-gated; it would fire while the composer is typing", b.Pattern)
		}
	}
	var _ interface{ IsFocused() bool } = p
}

// IsFocused reads the element, because that is where the framework keeps focus.
func TestScrollPaneReportsElementFocus(t *testing.T) {
	p := filledPane(t, 4)

	if p.IsFocused() {
		t.Error("a pane that has never rendered claims focus")
	}
	el := layOut(t, p, 40, 8)
	if p.IsFocused() {
		t.Error("an unfocused element makes the pane claim focus")
	}
	el.Focus()
	if !p.IsFocused() {
		t.Error("the element is focused and the pane says otherwise")
	}
}

// A scrollable element is focusable but not a tab stop by default, so the pane says focusable
// explicitly. Without it Tab would skip straight past the transcript to the composer.
func TestScrollPaneIsATabStop(t *testing.T) {
	el := layOut(t, filledPane(t, 4), 40, 8)

	if !el.IsTabStop() {
		t.Error("the pane is not in the Tab ring")
	}
}

// wheel builds one notch at a point.
func wheel(button gotui.MouseButton, x, y int) gotui.MouseEvent {
	return gotui.MouseEvent{Button: button, Action: gotui.MousePress, X: x, Y: y}
}

// The wheel scrolls the pane under the POINTER. Every pane sees every event — the framework walks
// mouse listeners until one consumes it — so hit-testing is what keeps two panes from both moving.
func TestScrollPaneWheelScrollsWhenHit(t *testing.T) {
	p := filledPane(t, 40)
	layOut(t, p, 40, 8)

	if !p.HandleMouse(wheel(gotui.MouseWheelUp, 5, 3)) {
		t.Fatal("a wheel notch inside the pane was not consumed")
	}
	if p.sticky.Get() {
		t.Error("scrolling up left the pane stuck to the bottom")
	}
	up := p.offset.Get()

	layOut(t, p, 40, 8)
	p.HandleMouse(wheel(gotui.MouseWheelDown, 5, 3))
	if p.offset.Get() <= up {
		t.Errorf("offset after scrolling down = %d, want more than %d", p.offset.Get(), up)
	}
}

func TestScrollPaneIgnoresTheWheelElsewhere(t *testing.T) {
	p := filledPane(t, 40)
	layOut(t, p, 40, 8)
	before := p.scrollOffset()

	if p.HandleMouse(wheel(gotui.MouseWheelUp, 5, 99)) {
		t.Error("a notch far below the pane was consumed")
	}
	if p.scrollOffset() != before {
		t.Error("a notch outside the pane moved it anyway")
	}
}

// A click moves the keyboard here, so the wheel and Tab agree about where you are. It goes through
// the root's focusOn rather than Element.Focus: the latter sets the element's own flag without
// telling the focus manager, and the two then disagree for good.
func TestScrollPaneClickAsksForFocus(t *testing.T) {
	var asked *gotui.Ref
	box := gotui.NewRef()
	p := ScrollPane(Pane{Box: box, Focus: func(r *gotui.Ref) { asked = r }, Height: 8, Follow: true}, nil)
	layOut(t, p, 40, 8)

	if !p.HandleMouse(gotui.MouseEvent{Button: gotui.MouseLeft, Action: gotui.MousePress, X: 5, Y: 3}) {
		t.Fatal("a click inside the pane was not consumed")
	}
	if asked != box {
		t.Error("the click did not ask for the keyboard")
	}
}
