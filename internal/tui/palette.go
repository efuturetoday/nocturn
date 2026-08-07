package tui

import (
	"slices"
	"strings"
	"time"

	tui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/internal/chat"
)

// The command palette answers "what can I do from here" without the reader having to know the
// answer first. What it replaces was a set of slash commands that only worked if you already knew
// their names, plus two of them — /agents and /help — that answered by writing UI output into the
// TRANSCRIPT, where it sits between what you asked and what the assistant said and stays there.
//
// The slash commands are untouched and still work, including the deliberate rule that an unknown
// /word goes to the model. This is a second way in, not a replacement.

// paletteWidth is the dialog's width in cells, shared with the approval so the two overlays are the
// same object seen twice rather than two different ones.
const paletteWidth = 72

// paletteVerbWidth is the fixed first column, so the names line up and the eye can run down them
// instead of reading each row from the left.
const paletteVerbWidth = 24

// paletteLimit is how many conversations the picker offers. The list on the left is where you
// BROWSE; this is where you jump to something recent, and a palette you have to scroll is one that
// has stopped being faster than the list.
const paletteLimit = 8

// The palette asks TWO questions, one at a time: what do you want to do, and then — only for the
// verbs that need one — which thing do you want to do it to.
//
// One question was the first shape and it was wrong. Offering "open X" and "delete X" for every
// conversation makes a list whose length is commands TIMES objects, where every verb appears once
// per object and every object twice; what the reader sees is the same two words repeating down the
// screen with the interesting part pushed into a second column. A palette is a list of things you
// can do. How many chats exist is not one of them.
type paletteStep uint8

const (
	stepRoot    paletteStep = iota // what do you want to do
	stepOpen                       // which conversation to read
	stepDelete                     // which conversation to throw away
	stepFire                       // which agent to start
	stepConfirm                    // are you sure — only ever reached from stepDelete
)

// title is the breadcrumb over the filter, so a picker says which question it is asking. Without it
// the second step is a list of names with no clue what Enter will do to them.
func (s paletteStep) title() string {
	switch s {
	case stepOpen:
		return " open "
	case stepDelete:
		return " delete "
	case stepFire:
		return " fire agent "
	case stepConfirm:
		return " confirm "
	default:
		return " commands "
	}
}

// paletteEntry is one row. run is a closure over the root rather than an id and a kind, so nothing
// has to be looked up twice — once to show the row and once to act on it.
type paletteEntry struct {
	// Verb is the first column: a command on the root step, the thing's own name on a picker. The
	// name goes first because on a picker the name is what is being chosen.
	Verb   string
	Detail string
	run    func()
}

func (a *app) openPalette() {
	a.goToStep(stepRoot)
	a.paletteOpen.Set(true)
}

// goToStep switches the question and clears the answer to the previous one. The query has to go: it
// was typed to narrow a different list, and carrying it over would open a picker that is already
// filtered by something the reader cannot see any more.
func (a *app) goToStep(s paletteStep) {
	a.paletteStep.Set(s)
	a.paletteQuery.Set("")
	a.paletteCursor.Set(0)
	if s != stepConfirm {
		a.paletteArmed.Set("")
	}
}

// closePalette puts the keyboard back where it was. The modal restores focus by index on its own,
// but only for a station that is still in the ring — asking for the composer explicitly is what
// survives a palette entry that changed the ring on its way out (a log pane opened, a run opened
// with no composer at all).
func (a *app) closePalette() {
	a.paletteOpen.Set(false)
	a.paletteStep.Set(stepRoot)
	a.paletteQuery.Set("")
	a.paletteArmed.Set("")
	if !a.readOnly() {
		a.focusOn(a.composer)
	}
}

// escape goes back one step rather than closing outright. Choosing "delete" and then realising you
// meant "open" is the ordinary case, and a dialog that throws the reader all the way out for it
// teaches them to be careful with a key that should cost nothing.
func (a *app) escapePalette() {
	if a.paletteStep.Get() == stepRoot {
		a.closePalette()
		return
	}
	a.goToStep(stepRoot)
}

// paletteKeys drive the dialog. They are all preempt and they all live on the root, for the two
// reasons the approval's keys do: Modal implements no PropsUpdater, so a cached modal would keep the
// KeyMap it was built with; and a trapping modal ends its own KeyMap with a catch-all that eats
// everything the preempt pass has not already taken.
//
// AnyRune is claimed here and nowhere else. The root may not bind bare letters in the chat mode —
// the composer owns typing there — but in this mode there is no composer to fight: the trap has
// moved focus into an overlay that holds nothing focusable, so the composer's own focus-gated
// bindings do not fire.
func (a *app) paletteKeys() tui.KeyMap {
	return tui.KeyMap{
		tui.OnPreemptStop(tui.KeyEscape, func(tui.KeyEvent) { a.escapePalette() }),
		tui.OnPreemptStop(tui.KeyUp, func(tui.KeyEvent) { a.movePalette(-1) }),
		tui.OnPreemptStop(tui.KeyDown, func(tui.KeyEvent) { a.movePalette(1) }),
		tui.OnPreemptStop(tui.KeyEnter, func(tui.KeyEvent) { a.runPalette() }),
		tui.OnPreemptStop(tui.KeyBackspace, func(tui.KeyEvent) { a.typePalette(0, true) }),
		tui.OnPreemptStop(tui.AnyRune, func(ke tui.KeyEvent) { a.typePalette(ke.Rune, false) }),
	}
}

// typePalette edits the filter. Every edit puts the cursor back on the first row: the list under it
// has just become a different list, and a cursor left at index three would be pointing at something
// the reader never chose.
func (a *app) typePalette(r rune, back bool) {
	q := a.paletteQuery.Get()
	switch {
	case back:
		if runes := []rune(q); len(runes) > 0 {
			q = string(runes[:len(runes)-1])
		}
	default:
		q += string(r)
	}
	a.paletteQuery.Set(q)
	a.paletteCursor.Set(0)
}

// movePalette walks the entries and stops at the ends rather than wrapping, the same way the
// conversation list does.
func (a *app) movePalette(d int) {
	n := len(a.paletteEntries())
	if n == 0 {
		return
	}
	a.paletteCursor.Set(min(max(a.paletteCursor.Get()+d, 0), n-1))
}

func (a *app) runPalette() {
	entries := a.paletteEntries()
	i := a.paletteCursor.Get()
	if i < 0 || i >= len(entries) {
		return
	}
	entries[i].run()
}

// paletteEntries is what the dialog shows, already filtered. It is rebuilt per render rather than
// held in a state: everything it is made of — the agents, the conversations, which step is up —
// already lives somewhere, and a second copy would be the one that goes stale.
func (a *app) paletteEntries() []paletteEntry {
	var all []paletteEntry
	switch a.paletteStep.Get() {
	case stepOpen:
		all = a.conversationEntries(func(kind, id string) func() {
			return func() {
				a.closePalette()
				a.openIn(kind, id)
			}
		})
	case stepDelete:
		all = a.conversationEntries(func(_, id string) func() {
			return func() {
				a.paletteArmed.Set(id)
				a.goToStep(stepConfirm)
			}
		})
	case stepFire:
		all = a.agentEntries()
	case stepConfirm:
		// One row, and it names what it will destroy. Anything other than Enter on it — Escape, or
		// walking back — is a refusal, which is the right default for the only act in this UI that
		// cannot be taken back.
		id := a.paletteArmed.Get()
		name := short(id)
		if m, ok := a.metaFor(id); ok {
			name = rowLabel(m)
		}
		return []paletteEntry{{
			Verb:   "delete " + oneLine(name, 16),
			Detail: "Enter confirms · Esc keeps it · this cannot be undone",
			run:    func() { a.deleteChat(id) },
		}}
	default:
		all = a.rootEntries()
	}
	return filterEntries(all, a.paletteQuery.Get())
}

// rootEntries is the list of things that can be done. Verbs that need a subject hand over to a
// picker instead of acting, and say so with a trailing "…" — the same promise the ellipsis makes in
// every menu ever written.
func (a *app) rootEntries() []paletteEntry {
	out := []paletteEntry{
		{Verb: "new chat", Detail: "start a fresh conversation", run: func() {
			a.closePalette()
			a.newChat()
		}},
		{Verb: "open chat…", Detail: "read a conversation you have had", run: func() {
			a.goToStep(stepOpen)
		}},
	}
	if a.ready() && len(a.ws.Agents()) > 0 {
		out = append(out, paletteEntry{
			Verb:   "fire agent…",
			Detail: "run one of this workspace's agents now",
			run:    func() { a.goToStep(stepFire) },
		})
	}
	return append(out,
		paletteEntry{Verb: "workspace", Detail: "what this assistant can do, and what is broken", run: func() {
			a.closePalette()
			a.toggleInspect()
		}},
		paletteEntry{Verb: "logs", Detail: "show or hide the log pane", run: func() {
			a.closePalette()
			a.toggleLogs()
		}},
		paletteEntry{Verb: "delete chat…", Detail: "throw a conversation away for good", run: func() {
			a.goToStep(stepDelete)
		}},
		paletteEntry{Verb: "quit", Detail: "leave nocturn", run: a.quit})
}

// agentEntries are the agents that can be started. An agent fired from here gets no task, because
// there is nowhere to type one: /fire <name> <task> is still how you say what it should do.
func (a *app) agentEntries() []paletteEntry {
	if !a.ready() {
		return nil
	}
	agents := a.ws.Agents()
	out := make([]paletteEntry, 0, len(agents))
	for _, ag := range agents {
		detail := "no description"
		if ag.Description != "" {
			detail = ag.Description
		}
		out = append(out, paletteEntry{
			Verb:   ag.Name,
			Detail: detail,
			run: func() {
				a.closePalette()
				a.fire(ag.Name)
			},
		})
	}
	return out
}

// conversationEntries is the picker, shared by "open" and "delete" because they are choosing from
// the same set — only what happens afterwards differs, and that is the act the caller passes in.
//
// A row reads the way its row in the sidebar reads: the name, then who started it and how long ago.
// The id is deliberately NOT here. It is what /open takes because a command line needs something to
// type; a list you point at needs something to recognise, and four hex digits are not that.
func (a *app) conversationEntries(act func(kind, id string) func()) []paletteEntry {
	if !a.ready() {
		return nil
	}
	metas := append(slices.Clone(a.chats.Get()), a.runs.Get()...)
	slices.SortFunc(metas, func(x, y chat.Meta) int { return y.Updated.Compare(x.Updated) })
	if len(metas) > paletteLimit {
		metas = metas[:paletteLimit]
	}

	now := time.Now()
	out := make([]paletteEntry, 0, len(metas))
	for _, m := range metas {
		kind, who := "user", "you"
		if m.Source == chat.SourceAgent {
			kind, who = "agent", m.Agent
		}
		out = append(out, paletteEntry{
			Verb:   oneLine(rowLabel(m), paletteVerbWidth),
			Detail: who + " · " + ago(m.Updated, now),
			run:    act(kind, m.ID),
		})
	}
	return out
}

// deleteChat throws a conversation away. It closes the palette first so the confirmation cannot be
// pressed twice, and clears the transcript when what was deleted is what is on screen — leaving it
// there would show a conversation that no longer exists and let a message be sent into it.
func (a *app) deleteChat(id string) {
	a.closePalette()
	if !a.ready() {
		return
	}
	kind := "user"
	if m, ok := a.metaFor(id); ok && m.Source == chat.SourceAgent {
		kind = "agent"
	}
	if err := a.ws.ChatManager(kind).Delete(id); err != nil {
		a.say("delete " + short(id) + ": " + err.Error())
		return
	}
	if a.activeID.Get() == id {
		a.newChat()
	}
	a.refreshLists()
	a.say("deleted " + short(id))
}

// filterEntries keeps the rows the query occurs in, matched over the verb and the detail together so
// that typing "fire" and typing an agent's name both work. Case-insensitive, substring, no ranking:
// the list is short enough that a clever order would only make it unpredictable.
func filterEntries(entries []paletteEntry, query string) []paletteEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return entries
	}
	out := make([]paletteEntry, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Verb+" "+e.Detail), q) {
			out = append(out, e)
		}
	}
	return out
}
