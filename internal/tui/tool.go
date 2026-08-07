package tui

import (
	tui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/internal/tui/transcript"
)

// A call's line in the transcript is a summary: the name, sixty runes of its arguments, the time it
// took, forty runes of what came back. That is the right amount for reading an answer — and the
// wrong amount the moment a call is the thing you are actually asking about, which is exactly when
// something went wrong.
//
// So the line opens. Clicking one shows the WHOLE input and the WHOLE output in an overlay, the way
// the mobile app's tool frame does; unlike the mobile app it is an overlay rather than an in-place
// expansion, because a transcript that grows under the reader takes their place with it — a finger
// on a phone has no scroll position to lose, and a reader three screens up does.
//
// The overlay is the fourth of its kind and behaves like the other three: Escape closes, what was
// underneath stays, and its pane holds the keyboard so j and k scroll a long result.

// The overlay's box. Sized rather than full-screen: what it shows is two blocks of prose, and text
// set across a whole wide terminal is text whose next line nobody finds. Seventy-six is about as
// wide as a line stays readable; twenty rows is enough to see a result's shape without covering the
// conversation it belongs to.
const (
	toolDetailWidth  = 76
	toolDetailHeight = 20
)

// toolKey identifies a call within the conversation on screen.
//
// The call's own id is NOT enough, and that is not a theoretical worry. The id comes from a counter
// carried in the session's context (agentkit's ToolSet.Call), so it restarts at one whenever a fresh
// session is opened — which the manager does after ten idle minutes, and every process does at
// startup. Replaying a chat that spans two sessions therefore hands out the same id twice, and a ref
// map keyed by it alone would have a click on one call open another from an earlier turn.
//
// The block index is what disambiguates: two calls with one id are always in different turns.
func toolKey(block int, id uint64) uint64 { return uint64(block)<<32 | id&0xffffffff }

// forgetTools drops the refs when the conversation on screen changes. Keeping them would leave the
// map growing for the life of the process and — worse — let a click match an element belonging to a
// transcript nobody is looking at any more. A RefMap offers no delete, so the map is replaced; the
// template reads the field afresh on every render, so the next frame binds into the new one.
func (a *app) forgetTools() { a.toolRefs = tui.NewRefMap[uint64]() }

// toolAt finds the call whose line is under the pointer, if any. It walks the view rather than the
// ref map because the view is what is on SCREEN: a ref may still hold an element from a block that
// has since been rebuilt, and the view is the list of calls the reader can actually see.
func (a *app) toolAt(x, y int) (transcript.Tool, bool) {
	// Outside the transcript's own box nothing counts, which is what stops a line scrolled out of
	// sight — whose element still has coordinates — from being hit. The pane itself is a top-level
	// element, so ContainsPoint is right for IT.
	box := a.body.El()
	if box == nil || !box.ContainsPoint(x, y) {
		return transcript.Tool{}, false
	}

	// The point has to be moved into the pane's CONTENT space before it is offered to a child, and
	// that is not a nicety — ContainsPoint is off by the pane's border and padding otherwise.
	//
	// A child's rect is measured from its scroll container's content box, starting at zero.
	// ContainsPoint converts a screen point by adding every scrollable ancestor's offset
	// (element_focus.go) and stops there: it never subtracts where that content box begins. For a
	// top-level element the two agree, which is why the panes hit-test themselves correctly. For a
	// line INSIDE a bordered, padded, scrolled pane the answer is one row and one column out — the
	// click lands on the line below the one under the pointer, and on the last visible line it lands
	// on nothing at all.
	//
	// Subtracting the content origin here leaves ContainsPoint to add the scroll, which is the half
	// it does get right.
	content := box.ContentRect()
	cx, cy := x-content.X, y-content.Y

	for i, b := range a.view.Get().Blocks {
		for _, t := range b.Tools {
			if el := a.toolRefs.Get(toolKey(i, t.ID)); el != nil && el.ContainsPoint(cx, cy) {
				return t, true
			}
		}
	}
	return transcript.Tool{}, false
}

// showTool raises the overlay. The call is copied in rather than looked up on every frame: a running
// call keeps changing, and a dialog whose content shifts under the reader is a dialog they have to
// read twice.
//
// The scroll position survives closing, so reopening the SAME call puts the reader back where they
// were — closing to look at the line above it and coming back is the ordinary move. A DIFFERENT call
// starts at the top, because a position measured in another call's output means nothing here.
func (a *app) showTool(t transcript.Tool) {
	if prev := a.tool.Get(); prev.ID != t.ID || prev.Name != t.Name {
		a.toolScroll.Set(0)
	}
	a.tool.Set(t)
	a.toolOpen.Set(true)
}

func (a *app) closeTool() {
	a.toolOpen.Set(false)
	if !a.readOnly() {
		a.focusOn(a.composer)
	}
}

// toolKeys close the overlay. Preempt, like every other overlay's: a trapping modal ends its own
// KeyMap with a catch-all that matches everything, and the preempt pass is the only one that runs
// before it. Scrolling is not here — the pane inside the modal owns j, k and the arrows through its
// own focus-gated bindings, which the dispatch table runs ahead of both passes.
func (a *app) toolKeys() tui.KeyMap {
	return tui.KeyMap{
		tui.OnPreemptStop(tui.KeyEscape, func(tui.KeyEvent) { a.closeTool() }),
		tui.OnPreemptStop(tui.KeyEnter, func(tui.KeyEvent) { a.closeTool() }),
	}
}

// toolTitle names the overlay after the call it is showing, so the box says what it is about without
// spending a line of its content on it.
func (a *app) toolTitle() string {
	t := a.tool.Get()
	if t.Name == "" {
		return " tool "
	}
	return " " + t.Name + " "
}
