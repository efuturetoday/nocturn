package tui

import (
	tui "github.com/grindlemire/go-tui"
)

// sidebar is the chat list. It owns where the list is scrolled to; the cursor and the rows belong to
// the root, because opening a chat from /open has to move the same cursor the arrow keys move.
//
// Like scrollPane it is a struct component so that its keys can be focus-gated, and its struct is
// declared in this .gsx file so `tui generate` writes the BindApp that binds offset.
type sidebar struct {
	box    *tui.Ref
	offset *tui.State[int]

	cursor   *tui.State[int]
	activeID *tui.State[string]
	filter   *tui.State[listFilter]
	// focused is whether this pane holds the keyboard, passed in rather than read off the element,
	// because the title has to be built before the element it titles exists.
	focused bool
	// rows, onSelect and focus are the root's methods. They are function fields, which UpdateProps
	// deliberately does not copy — that is fine here, because they are method values on the one
	// root that outlives every render.
	rows     func() []row
	sizes    func() (chats, runs int)
	onSelect func(row)
	focus    func(*tui.Ref)
}

// List is what the conversation list needs. A struct rather than nine positional arguments, which
// ended in `…, a.rows, a.listSizes, a.selectRow, at == regList)` — three functions and a bare bool
// whose meaning lived only in the signature.
type List struct {
	// Box is the caller's handle on the element, so it can move the keyboard here when a command
	// asks for the list.
	Box      *tui.Ref
	Focus    func(*tui.Ref)
	Cursor   *tui.State[int]
	ActiveID *tui.State[string]
	Filter   *tui.State[listFilter]
	// Rows, Sizes and OnSelect are the root's methods. Function fields, which UpdateProps
	// deliberately does not copy — fine here, because they are method values on the one root that
	// outlives every render.
	Rows     func() []row
	Sizes    func() (chats, runs int)
	OnSelect func(row)
	Focused  bool
}

// Sidebar builds the list.
func Sidebar(l List) *sidebar {
	return &sidebar{
		box:      l.Box,
		offset:   tui.NewState(0),
		cursor:   l.Cursor,
		activeID: l.ActiveID,
		filter:   l.Filter,
		focused:  l.Focused,
		rows:     l.Rows,
		sizes:    l.Sizes,
		onSelect: l.OnSelect,
		focus:    l.Focus,
	}
}

// The chips' counts. The sidebar asks the root, because the root owns both halves.
//
// Two single-value methods rather than one returning a pair: a template can only pass expressions,
// and neither a multi-value call nor a `x, y :=` binding survives into a component's argument list.
func (s *sidebar) chatCount() int { c, _ := s.sizes(); return c }
func (s *sidebar) runCount() int  { _, r := s.sizes(); return r }

// setFilter narrows or widens the list and starts the cursor over: the old index means nothing in a
// list that just changed length, and leaving it where it was would select something at random.
func (s *sidebar) setFilter(f listFilter) {
	if s.filter.Get() == f {
		return
	}
	s.filter.Set(f)
	s.cursor.Set(-1)
	s.offset.Set(0)
	s.move(1)
}

// cycleFilter is ← and →. It wraps, because three chips shown side by side are a ring the reader can
// see all of.
func (s *sidebar) cycleFilter(d int) {
	s.setFilter(listFilter((int(s.filter.Get()) + d + filterCount) % filterCount))
}

// chip is one filter in the header row: what it selects, how wide it is, and the padded text that
// fills it.
type chip struct {
	Filter listFilter
	Label  string
	Width  int
}

// chips is the filter row as it is drawn — same order, same widths, same text. Render walks this and
// so does the click arithmetic, so a pointer cannot land on a chip the eye does not see there. The
// three widths sum to the pane's content, which is what makes the row a full-width bar.
func chips(chats, runs int) []chip {
	return []chip{
		{filterChats, chipLabel("chats", chats, chipWidth), chipWidth},
		{filterRuns, chipLabel("agents", runs, lastChipWidth), lastChipWidth},
	}
}

// HandleMouse: the wheel scrolls the list, a click picks the row it landed on and opens it. One
// click rather than two, because a chat list is a list of things to open — and the click also moves
// the keyboard here, so the arrow keys carry on from where the pointer left off.
func (s *sidebar) HandleMouse(me tui.MouseEvent) bool {
	el := s.box.El()
	if el == nil || !el.ContainsPoint(me.X, me.Y) {
		return false
	}
	switch me.Button {
	case tui.MouseWheelUp:
		s.scrollBy(-wheelStep)
		return true
	case tui.MouseWheelDown:
		s.scrollBy(wheelStep)
		return true
	case tui.MouseLeft:
		// The bar first, and drags with it: grabbing it must not also pick whatever row the pointer
		// happens to be beside.
		if (me.Action == tui.MousePress || me.Action == tui.MouseDrag) && onScrollbar(el, me.X) {
			// No sticky flag: the list is a fixed set of rows and does not follow a tail, so where
			// the reader puts it is where it stays.
			scrollToPoint(el, s.offset, nil, me.Y)
			return true
		}
		if me.Action != tui.MousePress {
			return false
		}
		if s.focus != nil {
			s.focus(s.box)
		}
		s.clickAt(me.X, me.Y)
		return true
	}
	return false
}

// rowLines is how many screen lines one conversation takes: its name, and the dim line under it.
//
// The header above them is two lines and only the first is a target: the chips, then a blank. The
// blank is not decoration — without it the first conversation's name sits directly under a filled
// chip and reads as part of it, which is exactly the row the eye lands on first.
//
// Both constants are what maps between a row index and a y coordinate, in the click arithmetic and
// in reveal(), so they cannot drift from the template.
const (
	rowLines = 2
	// chipLines is the clickable part of the header.
	chipLines = 1
	// headerLines is all of it, spacer included: what row 0 begins after.
	headerLines = chipLines + 1
)

// clickAt routes a click to whatever it landed on. The content line is the distance from the top of
// the content plus however far the list is scrolled — read from the element, which is the only place
// the clamped truth lives.
//
// The header is checked FIRST and by its own comparison, not by letting the row arithmetic produce a
// negative index: (line - headerLines) / rowLines truncates toward zero in Go, so line 0 gave -1/2 = 0
// and a click on the filter chips opened the first conversation instead.
func (s *sidebar) clickAt(x, y int) {
	el := s.box.El()
	if el == nil {
		return
	}
	content := el.ContentRect()
	_, at := el.ScrollOffset()
	line := y - content.Y + at
	if line < 0 {
		return
	}
	if line < chipLines {
		s.clickFilter(x - content.X)
		return
	}
	if line < headerLines {
		return // the blank under the chips: not a chip, and not a row either
	}
	s.clickRow((line - headerLines) / rowLines)
}

// clickFilter picks the chip under the pointer, measured across the same labels the row is drawn
// from. dx is counted from the content's left edge, which is where the first chip starts.
func (s *sidebar) clickFilter(dx int) {
	left := 0
	for _, c := range chips(s.chatCount(), s.runCount()) {
		if dx >= left && dx < left+c.Width {
			s.setFilter(c.Filter)
			return
		}
		left += c.Width
	}
}

// clickRow opens the conversation at a list index. Clicking either of a row's two lines picks it —
// one click, because a chat list is a list of things to open.
func (s *sidebar) clickRow(i int) {
	rows := s.rows()
	if i < 0 || i >= len(rows) || !rows[i].Selectable() {
		return // a placeholder, or the padding below the last row
	}
	s.cursor.Set(i)
	s.onSelect(rows[i])
}

// scrollBy moves the list within its bounds. Unlike a scrollPane the list does not follow a tail —
// it is a fixed set of rows, and where the reader put it is where it stays.
func (s *sidebar) scrollBy(d int) {
	el := s.box.El()
	if el == nil {
		return
	}
	_, at := el.ScrollOffset()
	_, maxY := el.MaxScroll()
	s.offset.Set(min(max(at+d, 0), maxY))
}

// IsFocused answers the focus gate on the bindings below.
func (s *sidebar) IsFocused() bool {
	el := s.box.El()
	return el != nil && el.IsFocused()
}

// KeyMap walks the list, and only while the list holds the keyboard. This is why j is a binding at
// all: while the composer is focused its own OnFocused(AnyRune) wins the focus-gated pass, and j
// types a j.
func (s *sidebar) KeyMap() tui.KeyMap {
	return tui.KeyMap{
		tui.OnFocused(tui.Rune('j'), func(tui.KeyEvent) { s.move(1) }),
		tui.OnFocused(tui.Rune('k'), func(tui.KeyEvent) { s.move(-1) }),
		tui.OnFocused(tui.KeyDown, func(tui.KeyEvent) { s.move(1) }),
		tui.OnFocused(tui.KeyUp, func(tui.KeyEvent) { s.move(-1) }),
		tui.OnFocused(tui.KeyEnter, func(tui.KeyEvent) { s.selectRow() }),
		tui.OnFocused(tui.KeyLeft, func(tui.KeyEvent) { s.cycleFilter(-1) }),
		tui.OnFocused(tui.KeyRight, func(tui.KeyEvent) { s.cycleFilter(1) }),
	}
}

// move steps d selectable rows, skipping headers and placeholders, and stops at the ends rather
// than wrapping — a list that jumps from bottom to top loses the reader's place.
func (s *sidebar) move(d int) {
	rows := s.rows()
	i := s.cursor.Get()
	for range len(rows) {
		i += d
		if i < 0 || i >= len(rows) {
			return
		}
		if rows[i].Selectable() {
			s.cursor.Set(i)
			s.reveal()
			return
		}
	}
}

// reveal scrolls just enough to keep the selected row fully on screen — both of its lines, or the
// second one would sit half under the border. Everything here is in screen lines, which is what the
// element's scroll offset is measured in; the row index is converted once, on the way in.
func (s *sidebar) reveal() {
	el := s.box.El()
	if el == nil {
		return
	}
	visible := el.ContentRect().Height
	if visible <= 0 {
		return
	}
	_, maxY := el.MaxScroll()
	top := s.offset.Get()
	at := headerLines + s.cursor.Get()*rowLines
	switch {
	case at < top:
		top = at
	case at+rowLines > top+visible:
		top = at + rowLines - visible
	}
	s.offset.Set(min(max(top, 0), maxY))
}

func (s *sidebar) selectRow() {
	rows := s.rows()
	i := s.cursor.Get()
	if i < 0 || i >= len(rows) {
		return
	}
	s.onSelect(rows[i])
}

// The width is an attribute, not a w-32 class, only because the constant belongs next to the label
// budget it is computed with. The focus highlight is the framework's: a bordered element with no
// onFocus handler of its own turns cyan while it holds the keyboard.
templ (s *sidebar) Render() {
	<div ref={s.box} focusable scrollable={tui.ScrollVertical} scrollOffset={0, s.offset.Get()}
		class="flex-col shrink-0 px-1" width={sidebarWidth}
		borderTitle={paneTitle(" conversations ", s.focused)}
		border={paneBorder(s.focused)} borderStyle={paneBorderStyle(s.focused)}>
		// Inline, not @SidebarFilters(…): a component element compiles to app.Mount, and a sidebar
		// that mounts something unconditionally cannot be rendered without a real App — which needs
		// a terminal. The rows below mount too, but a test can pass an empty list; a header cannot
		// be left out.
		//
		// A full-width bar of three: the widths come from the pane's own content width and sum back to
		// it, so the row reaches both edges. Sized elements rather than grow, because the width has to
		// be a number the click arithmetic can read and the label can be padded to — a flex share is
		// known only after layout, and by then the text is already drawn.
		//
		// The chips start at the content's own left edge with no gutter, so a click needs nothing
		// subtracted, and there is no "←→" hint at the end: the hint line names those keys, and only
		// while the list actually holds the keyboard.
		<div class="flex shrink-0">
			for _, c := range chips(s.chatCount(), s.runCount()) {
				<span width={c.Width} textStyle={chipStyle(s.filter.Get() == c.Filter)}>{c.Label}</span>
			}
		</div>
		// The blank between the chips and the first conversation. Without it the first name sits
		// directly under a filled chip and reads as part of it — and that is the row the eye lands on
		// first. A sized element rather than an empty span, because a span with no text collapses to
		// nothing; headerLines counts this line, so the click arithmetic and reveal() know it is here.
		<div height={1} class="shrink-0"></div>
		for i, r := range s.rows() {
			@SidebarRow(r, i == s.cursor.Get(), r.ID != "" && r.ID == s.activeID.Get())
		}
	</div>
}
