package tui

import (
	"strings"
	"time"

	tui "github.com/grindlemire/go-tui"
)

// The UI is in exactly one MODE at a time, and inside the chat mode the keyboard is in exactly one
// REGION. Both are DERIVED — the mode from the flags it is drawn from, the region from what the
// framework says is focused — so there is no second copy of "where am I" that can drift from the
// first.
//
// The derivation is the point of this file. Everything the reader is told about the keyboard — which
// keys the root offers, what the hint line promises, which pane is marked — is computed from mode()
// and region(). What it replaces kept the bindings (KeyMap, assembled per render from conditional
// appends) and the promise about them (a fixed string in app.gsx naming six keys) side by side,
// where they drifted apart the moment either changed: the hint line offered Tab without saying where
// it went, through a ring that is three stations long or four depending on the log pane.

type mode uint8

const (
	modeChat    mode = iota // the list, the transcript and the composer
	modeInspect             // what this workspace can do
	modePalette             // the command palette
	modeTool                // one call's whole input and output
	modeApprove             // the gate's question
)

// mode is the one place the flags are read as a state. The order below IS the precedence, and it is
// not arbitrary: an approval has a turn blocked behind it, so it outranks a view somebody opened to
// read. Deriving rather than storing is what makes "two modes at once" unrepresentable.
func (a *app) mode() mode {
	switch {
	case a.asking.Get():
		return modeApprove
	case a.paletteOpen.Get():
		return modePalette
	case a.toolOpen.Get():
		return modeTool
	case a.inspectOpen.Get():
		return modeInspect
	default:
		return modeChat
	}
}

type region uint8

const (
	// regNone is nothing holding the keyboard: an overlay is up and has taken it, or focus fell off
	// the ring. It is a real state, not a failure — the modal traps focus into an overlay with
	// nothing focusable in it, which is how the composer stops swallowing keys while a question is up.
	regNone region = iota
	regList
	regTranscript
	regLogs
	regComposer
)

// regionName is what the hint line calls a region when it names where Tab goes.
func regionName(r region) string {
	switch r {
	case regList:
		return "list"
	case regTranscript:
		return "transcript"
	case regLogs:
		return "logs"
	case regComposer:
		return "composer"
	default:
		return "nowhere"
	}
}

// region answers where the keyboard is by asking the framework and matching its answer against the
// panes' refs. There is deliberately no state saying so: focus is restored BY INDEX after every
// render (focus.go refreshFromTree), so a flag of our own would be wrong exactly when a pane appears
// or disappears — which is the moment it is read.
func (a *app) region() region {
	if a.app == nil {
		return regNone
	}
	at, ok := a.app.Focused().(*tui.Element)
	if !ok || at == nil {
		return regNone
	}
	for _, m := range []struct {
		ref *tui.Ref
		reg region
	}{
		{a.side, regList},
		{a.body, regTranscript},
		{a.logView, regLogs},
		{a.composer, regComposer},
	} {
		if m.ref.El() == at {
			return m.reg
		}
	}
	return regNone
}

// hintState is what the hints need to know beyond the mode and the region. It is a value rather than
// the app so the hint lines are pure — they are the promise the key tables have to keep, and a
// promise that can only be checked by running a terminal cannot be checked at all.
type hintState struct {
	Running  bool // a turn is in flight, so cancelling means something
	LogOpen  bool // the log pane is on screen, which makes it a Tab station
	ReadOnly bool // an agent run is open: there is no composer to Tab to
	// Nested is a palette showing its second question, where Escape goes back one step instead of
	// closing. Saying "close" there would be a lie about the key it names.
	Nested bool
	// The workspace view's own depth: which page is up and whether the keyboard is going into its
	// filter. Escape means three different things across those, and the line has to say which.
	Section sectionID
	Typing  bool
}

// nextRegion is where Tab goes from here. The ring IS the element tree in order — list, transcript,
// the log pane when it is open, the composer when there is one — so this function and app.gsx's
// element order are two statements of the same fact, and the hint line is only honest while they
// agree.
func nextRegion(r region, s hintState) region {
	ring := []region{regList, regTranscript}
	if s.LogOpen {
		ring = append(ring, regLogs)
	}
	if !s.ReadOnly {
		ring = append(ring, regComposer)
	}
	for i, at := range ring {
		if at == r {
			return ring[(i+1)%len(ring)]
		}
	}
	return ring[0]
}

// hintsFor is the second line under the composer: what the keys mean RIGHT NOW. Every key it names
// has to be offered in the same mode — that is what TestHintsOnlyNameKeysThatExist checks — and no
// key that is offered may go unnamed anywhere it is the obvious next move.
func hintsFor(m mode, r region, s hintState) string {
	switch m {
	case modeApprove:
		return join("↑↓ pick", "Enter allow", "1-9 direct", "n or Esc deny")
	case modePalette:
		if s.Nested {
			return join("type to filter", "↑↓ pick", "Enter run", "Esc back")
		}
		return join("type to filter", "↑↓ pick", "Enter run", "Esc close")
	case modeTool:
		return join("↑↓ scroll", "g/G top/end", "Esc close")
	case modeInspect:
		switch {
		case s.Typing:
			return join("type to filter", "Enter keep", "Esc clear")
		case s.Section == sectionBoard:
			return join("1-7 open a section", "↑↓ scroll", "g/G top/end", "Esc close")
		case s.Section.Filterable():
			return join("↑↓ scroll", "g/G top/end", "/ filter", "Esc back", "1-7 another section")
		default:
			return join("↑↓ scroll", "g/G top/end", "Esc back", "1-7 another section")
		}
	}

	// The chat mode names what the region does, where Tab goes, and the two keys that are true
	// everywhere. It deliberately does NOT list every shortcut: Ctrl+K and Ctrl+L still work, and
	// they are in the palette, which is what "Ctrl+P commands" is a pointer to. A hint line that
	// names everything is a hint line nobody finishes reading — and the keys it dropped are exactly
	// the ones that now have a discoverable home.
	var parts []string
	switch r {
	case regList:
		parts = []string{"↑↓ walk", "Enter open", "←→ filter"}
	case regTranscript:
		parts = []string{"↑↓ scroll", "g/G top/end", "click a tool for its input and output"}
	case regLogs:
		parts = []string{"↑↓ scroll", "Ctrl+L close"}
	case regComposer:
		parts = []string{"Enter send", "Ctrl+N new"}
	default:
		parts = []string{"Tab takes the keyboard"}
	}
	if r != regNone {
		parts = append(parts, "Tab → "+regionName(nextRegion(r, s)))
	}
	if s.Running {
		parts = append(parts, "Ctrl+C cancel")
	}
	return join(append(parts, "Ctrl+P commands", "Ctrl+Q quit")...)
}

func join(parts ...string) string { return strings.Join(parts, " · ") }

// flashLife is how long a transient message stays. Long enough to read a sentence, short enough that
// nobody has to wonder whether "denied NetKind" is about the action they just took or the one
// before it — which is the failure mode of the single status string this replaces.
const flashLife = 4 * time.Second

// flashMsg is something that happened, as opposed to something that IS. A refusal, a usage line, the
// id of a run that was just fired: each is true at a moment and false afterwards, and none of them
// belongs in the same slot as where you are and what it cost.
type flashMsg struct {
	Text  string
	Until time.Time
}

// Live reports whether the message is still worth showing at now.
func (f flashMsg) Live(now time.Time) bool { return f.Text != "" && now.Before(f.Until) }

// say raises a transient message. It is the only way to write one, so nothing can leave a message on
// screen forever by forgetting a deadline.
func (a *app) say(text string) {
	a.flash.Set(flashMsg{Text: text, Until: time.Now().Add(flashLife)})
}

// expireFlash drops a message whose time is up. Called from the second-tick that already exists for
// the tool timers rather than from a timer of its own: a four-second message is allowed to linger
// for up to one extra second, and that is cheaper than a second clock.
func (a *app) expireFlash() {
	if f := a.flash.Get(); f.Text != "" && !f.Live(time.Now()) {
		a.flash.Set(flashMsg{})
	}
}

// hintLine is what the second line shows: a live message if there is one, otherwise the keys. They
// share a line because they are the same question — "what now" — answered by an event or by the
// state, and never both at once.
func (a *app) hintLine() string {
	if f := a.flash.Get(); f.Live(time.Now()) {
		return f.Text
	}
	return hintsFor(a.mode(), a.region(), a.hintState())
}

func (a *app) hintState() hintState {
	return hintState{
		Running:  a.view.Get().Running,
		LogOpen:  a.logOpen.Get(),
		ReadOnly: a.readOnly(),
		Nested:   a.paletteStep.Get() != stepRoot,
		Section:  a.inspectSection.Get(),
		Typing:   a.inspectTyping.Get(),
	}
}

// readOnly reports whether what is open is an agent run — a record of what an agent did, not a
// conversation to join. It has its own persona and cage, and a message typed into it would be
// answered by neither, so the composer is replaced by a line saying so rather than left there to
// refuse what was typed into it.
func (a *app) readOnly() bool { return a.activeKind.Get() == "agent" }

// paneTitle marks the pane holding the keyboard in its own border title. The framework already tints
// a focused border cyan, which says WHICH one at a glance; the marker survives a colourless terminal
// and reads the same way the sidebar's cursor does. It is the title rather than a borderTitleStyle
// because .gsx has no such attribute — only borderTitle, which takes an expression.
func paneTitle(title string, focused bool) string {
	if !focused {
		return title
	}
	return "▸" + title
}
