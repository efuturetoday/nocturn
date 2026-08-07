package tui

import (
	"fmt"
	"slices"
	"time"

	"github.com/efuturetoday/nocturn/internal/chat"
)

// The sidebar lists CONVERSATIONS and nothing else.
//
// A chat and an agent run are the same kind of THING — a transcript with a time of last activity —
// which is why one set of rows, one order and one badge serve both. They are shown a half at a time
// anyway, because they answer different QUESTIONS: "what did I ask it" and "what did it do while I
// was away". A list holding both answers the second only after the reader has read past the first,
// and the second is the one asked in the morning.
//
// Newest first inside each half, the way any mailbox is ordered. Which agent owns a run is a badge
// on its row rather than another section — one axis of sorting is enough for a list this size.
//
// Agents THEMSELVES are not here, and that is the point of the design. An agent is not something you
// read, it is something you run — it has no transcript, no time, no unread state, and Enter on one
// could only mean "now tell me what to do", which is a text field's job. Things you read are on the
// left; things you do are in the palette, where firing one lives. Keeping them apart also keeps the
// most-used list from being pushed off the bottom by a fixed block that is rarely touched.
type rowKind uint8

const (
	rowEmpty rowKind = iota
	rowChat
	rowRun
)

// row is one conversation in the list.
type row struct {
	Kind   rowKind
	ID     string
	Label  string
	Source string // the agent that owns a run; empty for a chat the person started
	When   string // last activity, in the shortest form that stays unambiguous
	Unread bool
}

// Selectable reports whether Enter does anything on this row.
func (r row) Selectable() bool { return r.Kind == rowChat || r.Kind == rowRun }

// listFilter is which conversations the list shows: the ones you had, or the ones the agents had.
// Two, and no combined view — they are looked for at different moments, "what did I ask it" against
// "what did it do while I was away", and a list holding both answers the second question only after
// the reader has read past the first.
//
// A chat and an agent run are still the same KIND of thing, which is why one set of rows, one order
// and one badge serve both. What differs is which question is being asked.
type listFilter uint8

const (
	filterChats listFilter = iota
	filterRuns
	filterCount = 2
)

func (f listFilter) String() string {
	if f == filterRuns {
		return "agents"
	}
	return "chats"
}

// emptyLabel says why the list is empty AND what to do about it, which depends on which half is
// being looked at — no agent runs is not the same fact as no chats.
func emptyLabel(f listFilter) string {
	if f == filterRuns {
		return "no agent runs yet — Ctrl+P to fire one"
	}
	return "no chats yet — just type"
}

// rows is the conversation list, newest activity first. A chat and an agent run are the same kind of
// thing — a transcript with a time of last activity — so they are one list in one order, the way any
// mail client shows a mailbox; who spoke first is the badge on the row.
func (a *app) rows() []row {
	if !a.ready() {
		return []row{{Kind: rowEmpty, Label: "opening…"}}
	}
	return mergeRows(a.chats.Get(), a.runs.Get(), a.filter.Get(), time.Now())
}

// mergeRows is the list itself, kept pure so the ordering and the filter can be checked without a
// workspace — which is the only way they can be checked at all, since opening one needs a vault, a
// network and a disk.
func mergeRows(chats, runs []chat.Meta, f listFilter, now time.Time) []row {
	var metas []chat.Meta
	if f != filterRuns {
		metas = append(metas, chats...)
	}
	if f != filterChats {
		metas = append(metas, runs...)
	}
	if len(metas) == 0 {
		return []row{{Kind: rowEmpty, Label: emptyLabel(f)}}
	}
	slices.SortFunc(metas, func(x, y chat.Meta) int { return y.Updated.Compare(x.Updated) })

	out := make([]row, 0, len(metas))
	for _, m := range metas {
		r := row{
			Kind:   rowChat,
			ID:     m.ID,
			Label:  rowLabel(m),
			When:   ago(m.Updated, now),
			Unread: m.Updated.After(m.Read),
		}
		if m.Source == chat.SourceAgent {
			r.Kind, r.Source = rowRun, m.Agent
		}
		out = append(out, r)
	}
	return out
}

// rowLabel is what the conversation is about. A chat is named after its first message; an agent run
// has no such line, so it falls back to the agent that produced it.
func rowLabel(m chat.Meta) string {
	switch {
	case m.Name != "":
		return m.Name
	case m.Agent != "":
		return m.Agent
	default:
		return short(m.ID)
	}
}

// ago is a coarse age. The list is scanned, not read: "3h" answers "is this the one I was just in?"
// in a glance, where a timestamp would have to be compared against the current time first.
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	default:
		return t.Format("2 Jan")
	}
}

// refreshLists reloads what the sidebar shows. Cheap enough to run on any chat activity: both
// listings are metadata the store already holds.
func (a *app) refreshLists() {
	if !a.ready() {
		return
	}
	if metas, err := a.ws.Chats().List(); err == nil {
		a.chats.Set(metas)
	}
	if runs, err := a.ws.AgentRuns(); err == nil {
		a.runs.Set(runs)
	}
	a.clampCursor()
}

// clampCursor keeps the cursor on a selectable row after the lists change under it.
func (a *app) clampCursor() {
	rows := a.rows()
	i := a.cursor.Get()
	if i >= 0 && i < len(rows) && rows[i].Selectable() {
		return
	}
	for j, r := range rows {
		if r.Selectable() {
			a.cursor.Set(j)
			return
		}
	}
	a.cursor.Set(-1)
}

// selectRow opens the conversation the sidebar's Enter landed on. It lives on the root, not on the
// sidebar, because everything it reaches for is the root's.
func (a *app) selectRow(r row) {
	switch r.Kind {
	case rowChat:
		a.open(r.ID)
	case rowRun:
		a.openRun(r.ID)
	}
}

// cursorOnto puts the cursor on the conversation that was just opened, switching to the half it
// lives in first — otherwise opening an agent run by id would leave the sidebar pointing at some
// chat while the transcript shows the run, and the two would disagree about where you are.
func (a *app) cursorOnto(id string) {
	want := filterChats
	if m, ok := a.metaFor(id); ok && m.Source == chat.SourceAgent {
		want = filterRuns
	}
	a.filter.Set(want)
	if !a.pointAt(id) {
		a.cursor.Set(-1)
	}
}

func (a *app) pointAt(id string) bool {
	for i, r := range a.rows() {
		if r.Selectable() && r.ID == id {
			a.cursor.Set(i)
			return true
		}
	}
	return false
}

// listSizes is what the filter chips count. Both halves, always — the point of showing a count you
// are not looking at is that you can see it is empty without going there.
func (a *app) listSizes() (chats, runs int) {
	return len(a.chats.Get()), len(a.runs.Get())
}

// gotoList moves the keyboard to the conversation list — what /chats means now that the list is
// always on screen: take me there, rather than tell me.
func (a *app) gotoList() {
	a.refreshLists()
	a.focusOn(a.side)
}
