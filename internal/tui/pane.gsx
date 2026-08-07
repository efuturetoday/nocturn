package tui

import (
	"math"

	tui "github.com/grindlemire/go-tui"
)

// scrollPane is a focusable scroll box that follows its own tail. The transcript and the log are
// both one of these, so the scroll keys, the sticky rule and the focus highlight exist once.
//
// It is a struct component and not a pure templ for two reasons that both come from the framework.
// A component that owns state must be a struct component, and — the one that decides the whole
// focus model — only a component can carry focus-gated key bindings: the dispatch table wires
// OnFocused to the component's own IsFocused (dispatch.go buildDispatchTable), and refuses to build
// at all when a focus-gated binding has no IsFocused to ask ("mount each focusable widget as its
// own component instead of aggregating their KeyMaps onto a host").
//
// The struct lives in the .gsx file, not in a sibling .go file: `tui generate` writes BindApp only
// when it finds the struct declaration in the same file (generator_component.go findStructDecl), and
// without BindApp the States below stay unbound — State.Set then skips MarkDirty and nothing
// repaints.
type scrollPane struct {
	// box is the scrollable element. The owner passes it in so it can move the keyboard here;
	// the pane needs it for the scroll geometry.
	box *tui.Ref
	// offset is where the reader scrolled to, and sticky says they have not scrolled away yet.
	// Both are the pane's own business — scrolling the log must not unstick the transcript.
	offset *tui.State[int]
	sticky *tui.State[bool]

	// focus is the root's focusOn. A click has to move the keyboard the same way Tab does, and
	// Element.Focus would only set the element's own flag without telling the focus manager.
	focus func(*tui.Ref)

	title string
	// focused is whether this pane holds the keyboard, passed in rather than read off the element:
	// the border is chosen BEFORE the element exists, and the element's own flag is one frame stale.
	focused  bool
	height   int // rows; 0 means take whatever is left
	children []*tui.Element
}

// Pane is what a scroll box needs beyond its children. A struct rather than eight positional
// arguments: the call site read `(a.body, a.focusOn, title, 0, true, at == regTranscript, nil)`, and
// three of those are a bare bool or zero whose meaning lived only in this signature.
type Pane struct {
	// Box is the caller's handle on the element, so it can move the keyboard here.
	Box   *tui.Ref
	Focus func(*tui.Ref)
	Title string
	// Height in rows; zero grows to fill what is left.
	Height int
	// Follow starts the pane pinned to its tail. A transcript and a log grow at the bottom and that
	// is where the new material is; a PAGE has its beginning at the top, and opening it already
	// scrolled past everything shows an empty box.
	Follow bool
	// Focused is whether this pane holds the keyboard, passed in rather than read off the element:
	// the border is chosen BEFORE the element exists, and the element's own flag is one frame stale.
	Focused bool
	// Offset lets the OWNER hold the scroll position instead of the pane. Only the overlays use it,
	// and for one reason: an overlay's wheel has to be handled by the root, because the framework
	// walks mouse listeners in tree order and the transcript pane comes before anything inside a
	// modal — so a notch over the workspace view would scroll the conversation behind it. Left nil
	// the position stays here, which is what every pane in the main layout wants.
	Offset *tui.State[int]
}

// ScrollPane builds the pane.
func ScrollPane(p Pane, children []*tui.Element) *scrollPane {
	if p.Offset == nil {
		p.Offset = tui.NewState(0)
	}
	return &scrollPane{
		box:      p.Box,
		offset:   p.Offset,
		sticky:   tui.NewState(p.Follow),
		focus:    p.Focus,
		title:    p.Title,
		focused:  p.Focused,
		height:   p.Height,
		children: children,
	}
}

// HandleMouse scrolls the pane under the POINTER, not the one holding the keyboard — that is what
// a wheel means everywhere else. A click moves the keyboard here, so the wheel and Tab agree about
// where you are.
//
// Hit-testing is the guard that makes this composable: every pane sees every event (the framework
// walks all mouse listeners until one consumes it), so a pane that does not contain the pointer
// must decline rather than act.
func (p *scrollPane) HandleMouse(me tui.MouseEvent) bool {
	el := p.box.El()
	if el == nil || !el.ContainsPoint(me.X, me.Y) {
		return false
	}
	switch me.Button {
	case tui.MouseWheelUp:
		p.scrollBy(-wheelStep)
		return true
	case tui.MouseWheelDown:
		p.scrollBy(wheelStep)
		return true
	case tui.MouseLeft:
		// The bar is grabbable. A drag is checked alongside a press because they are the same
		// gesture continued — the framework reports motion only while a button is held, which is
		// exactly a drag and is the only motion it reports at all.
		if (me.Action == tui.MousePress || me.Action == tui.MouseDrag) && onScrollbar(el, me.X) {
			scrollToPoint(el, p.offset, p.sticky, me.Y)
			return true
		}
		if me.Action == tui.MousePress && p.focus != nil {
			p.focus(p.box)
			return true
		}
	}
	return false
}

// IsFocused is what makes the OnFocused bindings below fire for THIS pane and no other. The
// framework's focus lives on the element, so that is where the answer is read from.
func (p *scrollPane) IsFocused() bool {
	el := p.box.El()
	return el != nil && el.IsFocused()
}

// KeyMap scrolls, and only while this pane holds the keyboard. OnFocused stops propagation, and the
// dispatch table runs focus-gated stop handlers in a pass of their own ahead of everything else, so
// two panes can both claim j without conflicting — at most one of them is focused.
func (p *scrollPane) KeyMap() tui.KeyMap {
	return tui.KeyMap{
		tui.OnFocused(tui.Rune('j'), func(tui.KeyEvent) { p.scrollBy(1) }),
		tui.OnFocused(tui.Rune('k'), func(tui.KeyEvent) { p.scrollBy(-1) }),
		tui.OnFocused(tui.KeyDown, func(tui.KeyEvent) { p.scrollBy(1) }),
		tui.OnFocused(tui.KeyUp, func(tui.KeyEvent) { p.scrollBy(-1) }),
		tui.OnFocused(tui.KeyPageDown, func(tui.KeyEvent) { p.scrollBy(10) }),
		tui.OnFocused(tui.KeyPageUp, func(tui.KeyEvent) { p.scrollBy(-10) }),
		tui.OnFocused(tui.Rune('g'), func(tui.KeyEvent) { p.scrollTop() }),
		tui.OnFocused(tui.KeyHome, func(tui.KeyEvent) { p.scrollTop() }),
		tui.OnFocused(tui.Rune('G'), func(tui.KeyEvent) { p.scrollEnd() }),
		tui.OnFocused(tui.KeyEnd, func(tui.KeyEvent) { p.scrollEnd() }),
	}
}

// scrollOffset is what the element is rendered with. While the pane is sticky it asks for MaxInt and
// the layout clamps it to the real bottom (WithScrollOffset: "clamped to valid range during
// layout"), which is what makes following the tail cost nothing: no watcher, no second pass, and no
// window between appending a line and pinning to it.
func (p *scrollPane) scrollOffset() int {
	if p.sticky.Get() {
		return math.MaxInt
	}
	return p.offset.Get()
}

// scrollBy moves the pane and re-sticks it once it reaches the bottom, so scrolling back to the end
// resumes following without a separate key. The current position is read from the ELEMENT rather
// than from offset: while sticky, offset holds MaxInt and only the element knows where that landed.
func (p *scrollPane) scrollBy(d int) {
	el := p.box.El()
	if el == nil {
		return
	}
	_, at := el.ScrollOffset()
	_, maxY := el.MaxScroll()
	y := min(max(at+d, 0), maxY)
	p.offset.Set(y)
	p.sticky.Set(y >= maxY)
}

// onScrollbar reports whether x is the column the scrollbar is drawn in: the last column of the
// content area, which the bar OVERLAYS rather than being given a column of its own (which is why
// every pane owes its right edge a blank — see scrollbarGap).
//
// One column is a thin target, and it is the target the framework draws. Widening it here would
// mean claiming a column the content is using.
func onScrollbar(el *tui.Element, x int) bool {
	r := el.ContentRect()
	return x == r.Right()-1
}

// scrollToPoint maps a point on the track to a position in the content: the top of the track is the
// beginning, the bottom is the end, everything between is proportional.
//
// Deliberately NOT thumb-relative. Following the thumb would mean re-deriving the framework's own
// thumb-height formula here, and a copy of a drawing rule is a copy that goes quietly wrong the day
// the rule changes — the point would stop matching the block under it and nobody would know why.
// Jumping to the position clicked needs nothing but the track, and is what a click on a bar means in
// most things that have one.
func scrollToPoint(el *tui.Element, offset *tui.State[int], sticky *tui.State[bool], y int) {
	_, maxY := el.MaxScroll()
	track := el.ContentRect()
	if maxY <= 0 || track.Height <= 1 {
		return // nothing to scroll, or no track to aim at
	}
	at := min(max(y-track.Y, 0), track.Height-1) * maxY / (track.Height - 1)
	offset.Set(at)
	if sticky != nil {
		sticky.Set(at >= maxY)
	}
}

func (p *scrollPane) scrollTop() {
	p.sticky.Set(false)
	p.offset.Set(0)
}

func (p *scrollPane) scrollEnd() { p.sticky.Set(true) }

// The two branches differ only in how the box is sized, and that cannot be one element with a
// computed class: Tailwind classes are resolved by the generator at build time, so a class={...} is
// silently dropped. Sizing that varies per instance has to be a typed attribute, and "grow" and
// "fixed height" are two different attributes.
//
// The border is OURS, through the typed border and borderStyle attributes rather than a class: a
// class is resolved by the generator at build time and cannot vary, and this one has to. Leaving it
// to the framework was the previous answer — a bordered element with no onFocus handler gets a
// built-in highlight — but that highlight is rounded and in the terminal's own cyan, which is a
// shape the rest of this UI does not use and a colour this program did not choose.
templ (p *scrollPane) Render() {
	if p.height > 0 {
		<div ref={p.box} focusable scrollable={tui.ScrollVertical} scrollOffset={0, p.scrollOffset()}
			class="flex-col shrink-0 px-1" height={p.height} borderTitle={p.title}
			border={paneBorder(p.focused)} borderStyle={paneBorderStyle(p.focused)}>
			{children...}
		</div>
	} else {
		<div ref={p.box} focusable scrollable={tui.ScrollVertical} scrollOffset={0, p.scrollOffset()}
			class="flex-col grow min-h-0 px-1" borderTitle={p.title}
			border={paneBorder(p.focused)} borderStyle={paneBorderStyle(p.focused)}>
			{children...}
		</div>
	}
}
