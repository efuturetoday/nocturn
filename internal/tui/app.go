package tui

import (
	"fmt"
	"strings"
	"time"

	tui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/tools"
	"github.com/efuturetoday/nocturn/internal/tui/transcript"
)

// The root component's behaviour. Its struct, its constructor and its template are in app.gsx —
// the struct has to live there for the generator to write BindApp (see the comment on the type).

// ready reports whether the workspace has finished opening. Until it has, the UI is drawn and takes
// keys — that is the whole point of drawing before loading — but there is nothing to send to.
func (a *app) ready() bool { return a.ws != nil }

// onOpened receives the workspace the opener built. A failure is shown and left on screen: without
// a workspace nothing else in the UI can do anything, and quitting out from under the reader would
// take the reason with it.
func (a *app) onOpened(o opened) {
	if o.err != nil {
		a.say("workspace: " + o.err.Error())
		a.notice("could not open the workspace: " + o.err.Error() + " — Ctrl+Q to quit")
		return
	}
	a.ws = o.ws
	a.refreshLists()
	a.say("ready — type to start")
}

// KeyMap is ONE TABLE PER MODE, chosen by mode(). It used to be one table with conditional appends,
// which is how a key could be offered in a state its hint line never mentioned and withheld in one
// where it did. A mode now owns its keys outright, hintsFor owns the promise about them, and a test
// holds the two together.
//
// Ctrl+Q and Ctrl+C sit outside the switch and preempt, because a modal ends its KeyMap with a
// catch-all that swallows every key the preempt pass has not already taken. Quitting and cancelling
// have to survive that. Everything else deliberately does not: what is on screen is what the turn is
// waiting for.
func (a *app) KeyMap() tui.KeyMap {
	km := tui.KeyMap{
		tui.OnPreemptStop(tui.Rune('q').Ctrl(), func(tui.KeyEvent) { a.quit() }),
		tui.OnPreemptStop(tui.KeyCtrlC, func(tui.KeyEvent) { a.cancel() }),
	}
	// Both overlays keep their keys HERE rather than on the modal's keyMap attribute: Modal
	// implements no PropsUpdater, so a cached modal keeps the KeyMap its factory produced the first
	// time one opened and would still be answering that question.
	switch a.mode() {
	case modeApprove:
		return append(km, a.approvalKeys()...)
	case modePalette:
		return append(km, a.paletteKeys()...)
	case modeTool:
		return append(km, a.toolKeys()...)
	case modeInspect:
		return append(km, a.inspectKeys()...)
	}
	return append(km,
		tui.OnStop(tui.KeyTab, func(ke tui.KeyEvent) { ke.App().FocusNext() }),
		tui.OnStop(tui.KeyTab.Shift(), func(ke tui.KeyEvent) { ke.App().FocusPrev() }),
		tui.OnStop(tui.Rune('n').Ctrl(), func(tui.KeyEvent) { a.newChat() }),
		tui.OnStop(tui.Rune('l').Ctrl(), func(tui.KeyEvent) { a.toggleLogs() }),
		tui.OnStop(tui.Rune('k').Ctrl(), func(tui.KeyEvent) { a.toggleInspect() }),
		tui.OnStop(tui.Rune('p').Ctrl(), func(tui.KeyEvent) { a.openPalette() }))
}

func (a *app) Watchers() []tui.Watcher {
	return []tui.Watcher{
		tui.Watch(a.loaded, a.onOpened),
		tui.Watch(a.feed.Items(), a.onItem),
		tui.Watch(a.approver.Asks(), a.onAsk),
		tui.Watch(a.approver.Cleared(), a.onAskCleared),
		tui.Watch(a.ring.Notify(), a.onLog),
		// The tool timers tick while a call runs; one second is the resolution they are read at.
		// The same tick keeps the workspace view current while it is open, so an MCP server that
		// reconnects or a note the assistant just wrote appears without closing and reopening it.
		tui.OnTimer(time.Second, func() {
			if a.view.Get().Running {
				a.view.Set(a.view.Get())
			}
			a.expireFlash()
			a.refreshInspect()
		}),
	}
}

// Every watcher lives on the root even though two of them feed panes that own their own state. The
// framework collects component watchers ONCE, after the first render (app_render.go,
// componentWatchersStarted), so a component that appears later — the log pane behind Ctrl+L — would
// have its Watchers() never started. The root is the one component guaranteed to be in that first
// tree.

// onItem folds one thing that happened into the view. Watcher callbacks run on the main loop, so
// this can read and write state without locking, and no event can slip between a chat switch's
// snapshot and its activeID.Set.
func (a *app) onItem(it Item) {
	switch it.Kind {
	case KindChat, KindAgent:
		if it.ChatID != a.activeID.Get() {
			a.noteBackground(it)
			return
		}
		a.view.Set(transcript.Apply(a.view.Get(), it.Event, time.Now()))
		// The turn's own clock, for the context bar. It is a plain field and not a State: the
		// second-tick that already redraws a running turn is what puts the new number on screen, so
		// nothing here needs to mark a frame dirty of its own.
		switch e := it.Event.(type) {
		case agentkit.TurnStart:
			if e.Frame == 0 {
				a.turnStart = time.Now()
			}
		case agentkit.TurnEnd:
			if e.Frame == 0 {
				a.tokens.Set(a.tokens.Get() + e.Tokens.Total)
				a.turnStart = time.Time{}
			}
		}
	case KindMeta:
		a.refreshLists()
	case KindNotify:
		a.view.Set(transcript.PushNotice(a.view.Get(), notice(it.Note)))
	}
}

// noteBackground reports a turn finishing in a chat the user is not looking at. Its events keep
// buffering in the manager, so switching to it later re-seeds the whole turn.
func (a *app) noteBackground(it Item) {
	if e, ok := it.Event.(agentkit.TurnEnd); ok && e.Frame == 0 {
		what := "chat"
		if it.Kind == KindAgent {
			what = "agent run"
		}
		a.say(fmt.Sprintf("%s %s finished", what, short(it.ChatID)))
	}
}

// onAsk raises the approval. Nothing is blurred here on purpose: the modal traps focus, which moves
// the keyboard into an overlay holding nothing focusable and so takes it away from the composer, and
// gives it back when the modal closes.
func (a *app) onAsk(ask *Ask) {
	a.ask.Set(ask)
	a.askCursor.Set(0)
	a.asking.Set(true)
}

// onAskCleared closes the modal when the ask it shows is over — the turn was cancelled, or the UI
// is shutting down. A different ask means this one was already answered.
func (a *app) onAskCleared(ask *Ask) {
	if a.ask.Get() == ask {
		a.closeAsk()
	}
}

// onLog refreshes the pane from the ring. It reads the snapshot here rather than in Render so a
// frame drawn for an unrelated reason does not take the ring's lock, and it does nothing at all
// while the pane is closed.
func (a *app) onLog(struct{}) {
	if !a.logOpen.Get() {
		return
	}
	a.logs.Set(a.ring.Snapshot())
}

// closeAsk drops the question and the flag together. They are two states for one fact only because
// <modal open> needs a *State[bool] and the dialog needs the Ask; setting them anywhere else would
// let them drift.
func (a *app) closeAsk() {
	a.ask.Set(nil)
	a.asking.Set(false)
}

// answer resolves the open ask with the i-th option and closes the modal.
func (a *app) answer(i int) {
	ask := a.ask.Get()
	if ask == nil {
		return
	}
	ask.Resolve(i)
	a.closeAsk()
	if i < 0 || i >= len(ask.Options) {
		a.say("denied " + ask.Action.Kind)
		return
	}
	a.say("allowed " + ask.Action.Kind + " — " + ask.Options[i].Label)
}

// approvalKeys answer the open ask. Escape denies rather than dismissing: closing the question has
// to answer it, or the turn waiting on it would never learn what happened.
//
// Two ways in, on purpose. A digit is the fastest answer and stays; the arrows and Enter are the
// gesture every other list in this UI uses, and a question that has to be read twice — once for what
// it asks and once for how to answer it — is a question answered carelessly.
func (a *app) approvalKeys() tui.KeyMap {
	km := tui.KeyMap{
		tui.OnPreemptStop(tui.KeyEscape, func(tui.KeyEvent) { a.answer(-1) }),
		tui.OnPreemptStop(tui.Rune('n'), func(tui.KeyEvent) { a.answer(-1) }),
	}
	ask := a.ask.Get()
	if ask == nil {
		return km
	}
	km = append(km,
		tui.OnPreemptStop(tui.KeyUp, func(tui.KeyEvent) { a.moveAsk(-1) }),
		tui.OnPreemptStop(tui.KeyDown, func(tui.KeyEvent) { a.moveAsk(1) }),
		tui.OnPreemptStop(tui.KeyEnter, func(tui.KeyEvent) { a.answer(a.askCursor.Get()) }))
	for i := range min(len(ask.Options), 9) {
		km = append(km, tui.OnPreemptStop(tui.Rune(rune('1'+i)), func(tui.KeyEvent) { a.answer(i) }))
	}
	return km
}

// moveAsk walks the options and stops at the ends. It does not wrap: the last option is the widest
// grant on offer, and an arrow that rolls from there back to "once" invites answering by rhythm.
func (a *app) moveAsk(d int) {
	ask := a.ask.Get()
	if ask == nil {
		return
	}
	a.askCursor.Set(min(max(a.askCursor.Get()+d, 0), len(ask.Options)-1))
}

// submit sends the composer's line, unless it is a slash command.
func (a *app) submit(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if strings.HasPrefix(line, "/") && a.command(line) {
		a.draft.Set("")
		return
	}
	if !a.ready() {
		a.say("still opening the workspace…")
		return
	}
	// A second message mid-turn would overwrite the manager's record of the first, corrupting the
	// replay any client gets when it reopens the chat. Refusing is the honest answer.
	//
	// There is no check for an agent run here any more: that state renders no composer at all, so
	// there is nothing to type into and nothing to refuse.
	if a.view.Get().Running {
		a.say("a turn is running — Ctrl+C to cancel it")
		return
	}
	a.draft.Set("")
	a.view.Set(transcript.PushUser(a.view.Get(), line))

	id := a.activeID.Get()
	if id == "" {
		id = chat.NewID()
		a.activeID.Set(id)
	}
	a.ws.Chats().Submit(id, line)

	v := a.view.Get()
	v.Running = true
	a.view.Set(v)
	a.turnStart = time.Now()
}

// open loads a user chat. openRun does the same for an agent run, which lives in the other manager.
func (a *app) open(id string) { a.openIn("user", id) }

func (a *app) openRun(id string) { a.openIn("agent", id) }

// openIn loads a chat, including one that is streaming right now: Open first so the session's pump
// exists, then the snapshot and the in-flight turn, which the fold replays through the same Apply
// the live stream uses. Both run on the event loop, so no event can be folded between the snapshot
// and the switch.
func (a *app) openIn(kind, id string) {
	if !a.ready() {
		a.say("still opening the workspace…")
		return
	}
	// Read before anything moves: whether the keyboard is about to lose the station it is standing on
	// can only be answered against the layout that is still on screen.
	was := a.region()
	mgr := a.ws.ChatManager(kind)
	mgr.Open(id)
	msgs, err := mgr.Transcript(id)
	if err != nil {
		a.say("open " + short(id) + ": " + err.Error())
		return
	}
	forest, err := mgr.Tools(id)
	if err != nil {
		a.say("open " + short(id) + ": " + err.Error())
		return
	}
	a.activeID.Set(id)
	a.activeKind.Set(kind)
	a.view.Set(transcript.Seed(msgs, forest, mgr.Inflight(id), time.Now()))
	a.tokens.Set(0)
	// A turn already in flight when it is opened has no start we know of. Zero means the clock shows
	// nothing rather than a number counting from the wrong moment.
	a.turnStart = time.Time{}
	a.forgetTools()
	a.ws.MarkRead(kind, id)
	a.cursorOnto(id)
	a.refreshLists()
	// Nothing is said here on purpose. Where you are and whether it is read-only are on the context
	// bar and in the pane's own title, permanently — a flash would only repeat them for four seconds
	// and then leave the question unanswered again.
	//
	// The keyboard deliberately stays where the reader put it. Opening from the list is how you
	// BROWSE — the next thing wanted is usually the next conversation, not a message — so pulling
	// focus to the composer here would take ↑↓ and ←→ away one keystroke after they were used, and
	// undo a click on the list in the same frame that answered it. The composer is one Tab away.
	//
	// The exception is the station that is about to stop existing. An agent run renders no composer,
	// and the focus manager restores focus BY INDEX after every render — so a keyboard sitting on the
	// composer would be restored to an index the ring no longer has, land nowhere, and leave the UI
	// taking no keys at all until something happened to press Tab.
	//
	// The test is "was NOT on the list" rather than "was on the composer", and the difference is the
	// palette: while an overlay is up the trap has taken the keyboard into it and nothing in the
	// ring holds it, so a run opened from there comes from regNone. Browsing is the one case worth
	// protecting, and browsing means the list.
	if a.readOnly() && was != regList {
		a.focusOn(a.body)
	}
}

func (a *app) newChat() {
	a.activeID.Set("")
	a.activeKind.Set("user")
	a.view.Set(transcript.View{})
	a.tokens.Set(0)
	a.turnStart = time.Time{}
	a.forgetTools()
	a.focusOn(a.composer)
}

// cancel stops the running turn. It never quits: Ctrl+C reaches us as a key because the framework
// clears ISIG, and a real SIGINT (kill -INT) is handled by the framework itself.
func (a *app) cancel() {
	if ask := a.ask.Get(); ask != nil {
		a.answer(-1)
		return
	}
	if !a.view.Get().Running || !a.ready() {
		a.say("nothing running — Ctrl+Q quits")
		return
	}
	a.ws.ChatManager(a.activeKind.Get()).Cancel(a.activeID.Get())
	a.say("cancelling…")
}

func (a *app) quit() {
	if a.app != nil {
		a.app.Stop()
	}
}

// toggleLogs opens or closes the log pane and hands the keyboard back to the composer.
// Opening a pane hands it the keyboard and closing gives it back, the same way Ctrl+K works: you
// opened it to read it, and a pane that appears without focus needs a Tab press to be useful. The
// hand-over is also load-bearing, not just convenient — the focus ring IS the element tree in
// order, and the manager restores focus by INDEX after a render, so a pane appearing above the
// composer would otherwise silently pass the keyboard to whatever slid into that index.
func (a *app) toggleLogs() {
	open := !a.logOpen.Get()
	a.logOpen.Set(open)
	if open {
		a.logs.Set(a.ring.Snapshot())
		a.focusOn(a.logView)
		return
	}
	a.focusOn(a.composer)
}

// focusOn asks for the keyboard to end up on the element behind ref. It only records the wish; the
// move happens in settleFocus, after the frame has been drawn.
//
// Deferring is the whole point. The tab ring IS the element tree in order, and the focus manager
// restores focus BY INDEX after every render, so a pane appearing or disappearing above the composer
// silently hands the keyboard to whatever slid into that index — the symptom being a Ctrl+L that
// leaves the log pane focused while the composer still believes it is. Moving focus before that
// render would be undone by it; moving it after is the only order that holds.
// It also marks the frame dirty, and that is not belt-and-braces. wantFocus is a plain field, not a
// State, so setting it changes nothing the renderer watches. A key press usually happens to move
// some State as well and drags a frame along with it; a CLICK that only asks for focus moves
// nothing — no frame is drawn, the post-render hook never runs, and the request sits there forever.
func (a *app) focusOn(ref *tui.Ref) {
	a.wantFocus = ref
	if a.app != nil {
		a.app.MarkDirty()
	}
}

// settleFocus runs as the app's post-render hook, once the tree and the tab ring agree again.
//
// The framework exposes no way to focus a chosen element — focusManager.SetFocus is unexported, and
// Element.Focus only sets a flag on the element without telling the manager, which leaves two
// elements looking focused and the keys going to the older one. FocusNext, FocusPrev and Focused are
// the whole public surface, so arriving means walking the ring.
//
// The walk stops when it has been somewhere twice. That is the exact condition, and it replaces a
// hand-counted bound of six — which was a guess at a ring that is three stations long, or four with
// the log pane, or two while the workspace view is up, and now also varies with whether the open
// conversation has a composer at all. A guess that is too small gives up before arriving; one that
// is too large spins past the target and lands somewhere arbitrary. Neither can happen to a walk
// that knows where it has been.
func (a *app) settleFocus() {
	ref := a.wantFocus
	if ref == nil || a.app == nil {
		return
	}
	a.wantFocus = nil
	want := ref.El()
	if want == nil {
		return
	}
	seen := make(map[*tui.Element]bool, 8)
	// A ring holds at most one empty position — the one FocusNext lands on when a lap finds nothing
	// else — so seeing a second one means it has nothing left to offer.
	blanks := 0
	for {
		at, _ := a.app.Focused().(*tui.Element)
		switch {
		case at == want:
			return
		case at == nil:
			if blanks++; blanks > 1 {
				return
			}
		case seen[at]:
			return // back where we have already been: the target is not on screen
		default:
			seen[at] = true
		}
		a.app.FocusNext()
	}
}

// wheelStep is how far one notch of the wheel moves a pane. Three lines is what terminals and
// editors settle on: one is imperceptible, a page loses the reader's place.
const wheelStep = 3

// HandleMouse is the root's share of the pointer, and it runs FIRST — the framework walks mouse
// listeners in tree order until one consumes the event, and the root is the first component in
// the tree. It therefore owns the two cases that are about the whole window rather than one pane.
//
// While an approval is up the root swallows everything. The dialog is a question the turn is
// blocked on; letting a click land on the transcript behind it would move the keyboard out from
// under the answer, and the modal's own key catch-all has no equivalent for the mouse.
func (a *app) HandleMouse(me tui.MouseEvent) bool {
	// While an overlay is up the root owns the pointer whole, and it scrolls the overlay ITSELF. The
	// event cannot simply be let through: the framework walks mouse listeners in TREE order, and the
	// transcript pane comes before anything inside a modal — so a notch over the workspace view would
	// scroll the conversation hidden behind it. Which is why those two panes let the root hold their
	// scroll position (see ScrollPane's offset argument).
	//
	// Everything that is not the wheel is swallowed. A click through a dialog would move the keyboard
	// out from under the question on screen.
	switch a.mode() {
	case modeChat:
	case modeTool:
		return a.overlayMouse(me, a.toolView, a.toolScroll)
	case modeInspect:
		return a.overlayMouse(me, a.inspectView, a.inspectScroll)
	default:
		return true
	}
	if me.Button == tui.MouseLeft && me.Action == tui.MousePress {
		if el := a.composer.El(); el != nil && el.ContainsPoint(me.X, me.Y) {
			a.focusOn(a.composer)
			return true
		}
		// A call's line opens onto the whole of what it sent and what it got back. Checked here, on
		// the root, because the transcript pane's own handler consumes a left press to take the
		// keyboard — a pane that answered first would swallow every click on a tool.
		if t, ok := a.toolAt(me.X, me.Y); ok {
			a.showTool(t)
			return true
		}
	}
	return false // the panes hit-test themselves
}

// overlayMouse gives an overlay's pane the same pointer its siblings in the main layout have — the
// wheel, and a scrollbar that can be clicked and dragged — but drives it FROM HERE.
//
// The pane cannot do this itself, and not for want of code: it has the handler already. The
// framework walks mouse listeners in tree order, and the transcript pane comes before anything
// inside a modal, so an event let through would be taken by the conversation hidden behind the
// dialog. So the root keeps the pointer and does the same work with the same two functions the panes
// use — which is why onScrollbar and scrollToPoint are package-level and not methods.
//
// It always consumes: a click that fell through to the layout underneath would move the keyboard out
// from under whatever is on screen.
func (a *app) overlayMouse(me tui.MouseEvent, ref *tui.Ref, offset *tui.State[int]) bool {
	el := ref.El()
	if el == nil {
		return true
	}
	switch me.Button {
	case tui.MouseWheelUp:
		a.scrollOverlayBy(el, offset, -wheelStep)
	case tui.MouseWheelDown:
		a.scrollOverlayBy(el, offset, wheelStep)
	case tui.MouseLeft:
		if (me.Action == tui.MousePress || me.Action == tui.MouseDrag) && onScrollbar(el, me.X) {
			// No sticky flag: an overlay opens at the top and does not follow a tail.
			scrollToPoint(el, offset, nil, me.Y)
		}
	}
	return true
}

// scrollOverlayBy moves the pane by d lines. The current position is read from the ELEMENT and not
// from the state: the pane clamps what it is given during layout, so the element is the only place
// the real position lives once the content is shorter than the box.
func (a *app) scrollOverlayBy(el *tui.Element, offset *tui.State[int], d int) {
	_, at := el.ScrollOffset()
	_, maxY := el.MaxScroll()
	offset.Set(min(max(at+d, 0), maxY))
}

// The chat list's column, and the room a label has inside it. A label longer than labelWidth is
// ellipsized rather than wrapped: the sidebar is one row per chat, and a wrapped name would push
// every row below it out of step with the cursor, which walks row indices.
const (
	// The list is wide enough to read a conversation's NAME in, which is the only thing anyone scans
	// it for. Cut short, every row ends in the same ellipsis and the list stops answering the
	// question it exists for.
	sidebarWidth = 40
	// sidebarContent is what is left inside the box: the width less its border and its padding.
	// Everything laid out by hand in this pane measures from here.
	sidebarContent = sidebarWidth - 4
	// The filter chips divide that width between them, so the row is a tab bar — full width, equal
	// parts, each one a block you can hit rather than a word with a highlight on it. The remainder
	// goes to the last chip instead of being left as a gap: "full width" has to be exactly true, or
	// the bar ends one column short of its own box and reads as a mistake.
	chipWidth     = sidebarContent / filterCount
	lastChipWidth = sidebarContent - (filterCount-1)*chipWidth
	// gutter is the blank column down the left of every row, so the marker on a selected row does
	// not shove its name sideways relative to its neighbours.
	gutter = 1
	// scrollbarGap is the room a scrollable pane owes its right edge: one column the scrollbar draws
	// in — it overlays the content rather than shrinking it — and one blank beside it, so a
	// truncated line ends in a visible "…" instead of pressing against the bar.
	scrollbarGap = 2
	// labelWidth is what a name may occupy: the column, less its border (2), its padding (2), the
	// gutter, the marker and the space after it, and the scrollbar's own margin.
	//
	// The last part is the whole point of measuring at all. A label cut to EXACTLY the space
	// available puts its "…" in the final column, and that column belongs to the scrollbar, so the
	// ellipsis is the one character that disappears — leaving something that reads as an ordinary
	// word which happens to end oddly, which is precisely what the ellipsis was there to prevent.
	labelWidth = sidebarWidth - 4 - gutter - 2 - scrollbarGap
)

// width is the composer's width in cells. The input is a widget with its own box, so it does not
// stretch: it is told how wide the window is.
func (a *app) width() int {
	if a.app == nil {
		return 80
	}
	w, _ := a.app.Size()
	return max(w, 20)
}

// bodyWidth is what the transcript has left after the sidebar and the two borders — the wrap width
// markdown is rendered at.
func (a *app) bodyWidth() int {
	return max(a.width()-sidebarWidth-4, 20)
}

// The context bar is what IS, in fixed slots: where you are, what is happening, what it has cost,
// which model. None of it is ever set — every field is read from the state that already decides it,
// so no code path can leave the bar describing a moment that has passed. That is the whole
// difference from the single status string it replaces, where "thinking…" and "denied NetKind" and
// "chat 8f2a" took turns in one slot and the reader could not tell which of them was still true.

// where is the conversation on screen, named the way its row in the list names it.
func (a *app) where() string {
	if !a.ready() {
		return "opening…"
	}
	id := a.activeID.Get()
	if id == "" {
		return "new chat"
	}
	if m, ok := a.metaFor(id); ok && m.Source == chat.SourceAgent {
		return "run " + short(id) + " · " + m.Agent
	}
	return "chat " + short(id) + " · you"
}

// activity is whether a turn is in flight, and for how long. The seconds are missing rather than
// wrong for a turn that was already running when it was opened: nothing on disk records when it
// began.
func (a *app) activity() string {
	if !a.view.Get().Running {
		return "idle"
	}
	if a.turnStart.IsZero() {
		return "⏵ thinking"
	}
	return "⏵ thinking " + time.Since(a.turnStart).Round(time.Second).String()
}

// metaFor finds a conversation in either list. Both are searched because an id is an id: which
// manager owns it is exactly what the caller is trying to find out.
func (a *app) metaFor(id string) (chat.Meta, bool) {
	for _, list := range [][]chat.Meta{a.chats.Get(), a.runs.Get()} {
		for _, m := range list {
			if m.ID == id {
				return m, true
			}
		}
	}
	return chat.Meta{}, false
}

// bodyTitle names the transcript pane after the conversation in it. A pane with a title is also a
// pane that can carry the focus marker, which is the other half of why it has one at all.
func (a *app) bodyTitle() string {
	id := a.activeID.Get()
	if id == "" {
		return " new chat "
	}
	name := short(id)
	if m, ok := a.metaFor(id); ok {
		name = rowLabel(m)
	}
	if a.readOnly() {
		return " " + oneLine(name, 40) + " · read only "
	}
	return " " + oneLine(name, 40) + " "
}

func notice(n tools.Notification) string {
	if n.Title == "" {
		return n.Message
	}
	return n.Title + " — " + n.Message
}

// short renders a chat id at the length the UI shows it.
func short(id string) string {
	if len(id) > 4 {
		return id[:4]
	}
	return id
}
