package tui

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	tui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/internal/tui/logring"
	"github.com/efuturetoday/nocturn/internal/tui/transcript"
)

// The components here are PURE — plain arguments, no state, no component elements — which is what
// lets a test render one with tui.Sprint and assert on the result without an App or a terminal.
// Anything needing <markdown>, <input> or <modal> has to live on a struct component instead.

// indentCells is a tool's nesting expressed as an empty box in front of it. It is a sized element
// rather than a pl-N class because Tailwind classes are resolved at code generation: a computed
// class={...} is silently dropped, and a computed indent is exactly that.
func indentCells(depth int) int {
	return min(depth, 6) * 2
}

// toolTiming renders a call's elapsed time: the frozen duration once it has returned, a coarse
// ticking clock while it runs.
func toolTiming(t transcript.Tool, now time.Time) string {
	if !t.Running {
		return t.Duration.Round(time.Millisecond).String()
	}
	if t.Started.IsZero() {
		return "…"
	}
	return now.Sub(t.Started).Round(time.Second).String()
}

// oneLine collapses a value to a single line and truncates it to width runes, so a multi-line
// argument or a long chat name cannot wrap and push the layout around. width 0 means no limit.
// Runes, not bytes: a name with an umlaut in it is not two characters longer.
func oneLine(s string, width int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if width <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}

// toolBudget splits a line's leftover width between the arguments and the result.
//
// Leftover, and measured — not a pair of constants. The line used to cut arguments at sixty runes
// and results at forty whatever the window was, so a wide terminal showed the same ellipsis as a
// narrow one and the space beside it stayed empty. What the line actually owes is its indent, the
// icon and name, the timing, and the gaps between them; everything after that is free, and it goes
// two-thirds to the input because the input is what identifies the call.
//
// A share is only taken from the other when there is nothing else to take it from. What each value
// NEEDS is measured first, and a value that needs less than its share leaves the rest to its
// neighbour — a fixed two-thirds/one-third was why `memory_read {"path": "people/oliver.md"}` sat
// beside forty columns of nothing while its result was still cut off.
//
// Only when both want more than the row has does the split matter, and then it flips on failure:
// the second half is a reason rather than a result, and the reason is the whole point of the line.
//
// The floor is 12: below that a value is not shortened, it is deleted, and an empty column reads as
// a call that was given nothing.
func toolBudget(t transcript.Tool, width int) (args, result int) {
	const timing, gaps, icon = 8, 5, 2
	free := width - indentCells(t.Depth) - icon - len([]rune(t.Name)) - timing - gaps
	if free < 24 {
		return 12, 12
	}

	out := t.Result
	if t.Err != "" {
		out = t.Err
	}
	// The collapsed length, because that is what is drawn: oneLine folds newlines away first, and a
	// value measured before folding would reserve room for characters that never appear.
	a, r := len([]rune(oneLine(t.Args, 0))), len([]rune(oneLine(out, 0)))

	switch {
	case a+r <= free:
		return a, r // both whole, and room to spare
	case r <= free/2:
		return free - r, r // the output fits; everything else goes to the input
	case a <= free/2:
		return a, free - a // the input fits; everything else goes to the output
	case t.Err != "":
		args = free / 3
	default:
		args = free * 2 / 3
	}
	return args, free - args
}

// ToolLine is one call: what ran, on what, how long it took, and a glance at what came back. The
// glance is deliberate — the whole of both is one click away, in ToolDetail — but it is as long a
// glance as the pane has room for.
//
// It carries NO ref, and that is a framework constraint rather than a preference. A pure templ
// builds its element in its CONSTRUCTOR; on a re-render Mount calls the factory again but then
// renders the CACHED instance and throws the fresh one away (mount.go). A `ref` here would therefore
// be rebound, every frame, to an element that is not in the tree — hit-testing it finds nothing, and
// only the very first frame would ever have worked. The ref goes on the wrapper blockView draws
// around this line, because blockView is a struct component and its Render is what actually runs.
templ ToolLine(t transcript.Tool, now time.Time, width int) {
	<div class="flex gap-1 shrink-0">
		<div width={indentCells(t.Depth)}></div>
		if t.Err != "" {
			<span class="text-red">{"✗ " + t.Name}</span>
		} else if t.Running {
			<span class="text-yellow">{"◐ " + t.Name}</span>
		} else {
			<span class="text-green">{"⏺ " + t.Name}</span>
		}
		<span class="font-dim">{oneLine(t.Args, toolArgsWidth(t, width))}</span>
		<span class="font-dim">{toolTiming(t, now)}</span>
		if t.Err != "" {
			<span class="text-red">{oneLine(t.Err, toolResultWidth(t, width))}</span>
		} else if !t.Running && t.Result != "" {
			<span class="font-dim">{"→ " + oneLine(t.Result, toolResultWidth(t, width))}</span>
		}
	</div>
}

// Two single-value helpers, because a template can only pass expressions: neither a multi-value call
// nor an `x, y :=` binding survives into an attribute.
func toolArgsWidth(t transcript.Tool, width int) int   { a, _ := toolBudget(t, width); return a }
func toolResultWidth(t transcript.Tool, width int) int { _, r := toolBudget(t, width); return r }

// ToolTail is the live text a sub-agent produces while its call runs — shown only while it runs,
// because once the call returns its Result is the answer.
templ ToolTail(t transcript.Tool) {
	<div class="flex shrink-0">
		<div width={indentCells(t.Depth + 1)}></div>
		<span class="font-dim">{oneLine(lastRunes(t.Stream, 200), 0)}</span>
	</div>
}

templ UserBlock(b transcript.Block) {
	<div class="flex gap-1">
		<span class="text-cyan font-bold">{"you ›"}</span>
		<span class="text-cyan">{b.Text}</span>
	</div>
}

templ NoticeBlock(b transcript.Block) {
	<div class="flex gap-1">
		<span class="text-magenta">{"◆"}</span>
		<span class="text-magenta">{b.Text}</span>
	</div>
}

// blockView renders one block. It is a struct component rather than a pure one because <markdown>
// is a component element and those mount against a receiver — the reason the assistant's answer
// cannot live in blocks.gsx's pure half.
type blockView struct {
	block transcript.Block
	now   time.Time
	width int
	// at is this block's index in the transcript. It is half the key a call's line is remembered by
	// — see toolKey.
	at int
	// toolRefs is where each call's line landed on screen. It is the ROOT's map, because the refs
	// have to outlive this block: a block is rebuilt on every frame and a click arrives between two
	// of them.
	toolRefs *tui.RefMap[uint64]
}

// BlockView renders a transcript block. at is its index in the transcript, width the column count
// available for wrapping markdown.
func BlockView(b transcript.Block, at int, now time.Time, width int, toolRefs *tui.RefMap[uint64]) *blockView {
	return &blockView{block: b, at: at, now: now, width: width, toolRefs: toolRefs}
}

// UpdateProps is NOT written here. The generator emits it (and the updatePropsFields helper it
// delegates to) for every templ receiver, and a hand-written one suppresses the generated version —
// which is how the copy list silently stops matching the struct the next time a field is added.

// The answer renders as markdown only once the turn has ENDED. While it streams, a half-written
// fence or table is not markdown yet, and re-parsing the whole answer on every token would pay for
// a structure that is about to change anyway.
templ (v *blockView) Render() {
	<div class="flex-col shrink-0">
		if v.block.Role == transcript.User {
			@UserBlock(v.block)
		} else if v.block.Role == transcript.Notice {
			@NoticeBlock(v.block)
		} else {
			// The ref lives on THIS wrapper, not inside ToolLine. blockView is a struct component, so
			// its Render runs on the cached instance and the element it builds here is the one in the
			// tree; a pure templ builds its element in its constructor, and a cached one hands back
			// the element it was born with while the factory's fresh copy — and any ref bound to it —
			// is discarded. The tail is inside the target too: it belongs to the same call, and a
			// reader aiming at a running call should not have to hit the first of its two lines.
			for _, t := range v.block.Tools {
				<div ref={v.toolRefs} key={toolKey(v.at, t.ID)} class="flex-col shrink-0">
					@ToolLine(t, v.now, v.width)
					if t.Running && t.Stream != "" {
						@ToolTail(t)
					}
				</div>
			}
			// The reasoning, while it is still reasoning. It has been folded all along and never
			// drawn, which is the difference between a pause that is working and a pause that is
			// stuck. It goes once the answer is there: at that point it is what the answer was made
			// from, and the answer says it better.
			if v.block.Pending && v.block.Think != "" {
				<div class="flex gap-1">
					<span class="font-dim">{"⋮"}</span>
					<span class="font-dim">{oneLine(lastRunes(v.block.Think, 160), 0)}</span>
				</div>
			}
			if v.block.Text != "" && v.block.Pending {
				<span>{v.block.Text}</span>
			} else if v.block.Text != "" {
				<markdown source={v.block.Text} width={v.width} />
			}
			if v.block.Pending && v.block.Text == "" {
				<span class="font-dim">{"…"}</span>
			}
			if v.block.Cancelled {
				<span class="font-dim">{"[cancelled]"}</span>
			} else if v.block.Err != "" {
				// Whole, and wrapped. It used to be cut to eighty runes, and a provider's message puts
				// the reason at the END — "…create stream: error, status code: 400, status: 4…" told
				// the reader that something failed and hid what, which is the one thing the line is
				// for. A turn that stopped is rare enough to be allowed three screen lines.
				<span class="text-red">{"[stopped: " + strings.ReplaceAll(v.block.Err, "\n", " ") + "]"}</span>
			}
		}
	</div>
}

// SidebarRow draws one line of the chat list. selected is the cursor, active is the chat currently
// in the transcript — two different things, and both have to be visible at once: you can walk the
// list while a turn keeps streaming in the chat you left.
// A conversation is two lines, not one. The first is what it is ABOUT, given the whole width; the
// second carries who started it and how long ago, dimmed. Cramming both onto one line is what forced
// a chat name to be cut at twenty characters — and the name is the only thing anyone scans for.
//
// The markers are three different facts and each gets its own column, because they co-occur: ▸ is
// where the cursor is, │ is the conversation currently in the transcript, • is unread. A single
// symbol standing for all three would leave the reader guessing which one it meant.
templ SidebarRow(r row, selected bool, active bool) {
	<div class="flex-col shrink-0">
		if r.Kind == rowEmpty {
			<span class="font-dim">{"  " + r.Label}</span>
		} else {
			// The gutter is a SIZED element, not a padded string: a span's leading spaces are
			// trimmed by the layout, so an alignment made of spaces silently collapses and the
			// column budget the label was cut to no longer matches where it starts.
			<div class="flex">
				<div width={gutter}></div>
				if selected {
					<span textStyle={cursorStyle()}>{"▸"}</span>
				} else if active {
					<span class="text-green">{"│"}</span>
				} else {
					<div width={1}></div>
				}
				<div width={1}></div>
				<span textStyle={rowStyle(selected, r.Unread)}>{oneLine(r.Label, labelWidth)}</span>
			</div>
			<div class="flex">
				<div width={gutter + 2}></div>
				if r.Kind == rowRun {
					<span class="text-magenta">{oneLine(r.Source, labelWidth-8)}</span>
				} else {
					<span class="font-dim">{"you"}</span>
				}
				<div width={1}></div>
				<span class="font-dim">{"· " + r.When}</span>
				if r.Unread {
					<div width={1}></div>
					<span class="text-yellow">{"•"}</span>
				}
			</div>
		}
	</div>
}

// The sidebar's filter chips are drawn INLINE in the sidebar's own template rather than being a
// component of their own, and that is the difference between a sidebar that can be rendered in a
// test and one that cannot: `@Something(…)` compiles to app.Mount, and mounting needs a real App,
// which needs a terminal. The row's two helpers live here with the rest of the styling.
//
// The list itself is ONE list — a chat and an agent run are both a transcript with a time of last
// activity, and who spoke first is the badge on the row. The chips narrow it for the two moments
// where the question really is one-sided, and "all" is where you start.
//
// The active chip is REVERSED — the terminal's own way of saying "this one" — rather than merely a
// different colour. Two shades of text next to each other is a difference you have to look for; a
// filled block is one you cannot miss. Each carries its count, so a half you are not looking at is
// visibly empty without going there.

// chipLabel centres a name in its share of the row and pads it out to the full width.
//
// The padding is what makes a tab bar out of three words. A reversed span paints the cells it has,
// so a label that stops at its last letter leaves the block ending mid-chip; padded, each third is a
// solid slab from edge to edge, which is both what a tab looks like and what a pointer can aim at.
//
// The count rides along, because a third of the list's width has room for it: it is how a half you
// are not looking at shows that it is empty without your having to go there.
func chipLabel(name string, n int, width int) string {
	r := []rune(fmt.Sprintf("%s %d", name, n))
	if len(r) >= width {
		return string(r[:width])
	}
	pad := width - len(r)
	return strings.Repeat(" ", pad/2) + string(r) + strings.Repeat(" ", pad-pad/2)
}

// The active chip is filled with the brand's own purple rather than reversed. Reverse swaps the
// terminal's default foreground and background, so the block came out whatever the user's colour
// scheme happens to call white — a slab that belongs to the terminal and not to this program. A
// named fill is the same colour everywhere and is the colour the rest of the product uses.
func chipStyle(active bool) tui.Style {
	if active {
		return tui.NewStyle().Background(accent).Foreground(onAccent).Bold()
	}
	return tui.NewStyle().Foreground(tui.BrightBlack)
}

// ContextBar is what IS: where you are, what is happening, what it has cost, which model. Fixed
// slots in a fixed order, so the eye goes to the same place for the same question every time — and
// none of it is ever written by a code path, only derived. The model sits on the right, apart from
// the rest: it is the one thing that does not change while you work.
//
// It takes the window width because justify-between needs something to spread across; the row is
// otherwise as wide as its text and the model would sit against it.
templ ContextBar(where string, activity string, tokens int, model string, width int) {
	<div class="flex justify-between px-1 shrink-0" width={width}>
		<div class="flex gap-1">
			<span>{where}</span>
			<span class="font-dim">{"·"}</span>
			<span textStyle={activityStyle(activity)}>{activity}</span>
			if tokens > 0 {
				<span class="font-dim">{"·"}</span>
				<span class="font-dim">{fmt.Sprintf("%d tok", tokens)}</span>
			}
		</div>
		<span class="font-dim">{model}</span>
	</div>
}

// A running turn is the one thing on the bar that is worth looking up for, so it is the only thing
// with a colour. Everything else stays plain: a bar where four fields compete is a bar nobody reads.
func activityStyle(activity string) tui.Style {
	if strings.HasPrefix(activity, "⏵") {
		return tui.NewStyle().Foreground(tui.Yellow)
	}
	return tui.NewStyle().Dim()
}

// ReadOnlyBar stands where the composer would be when what is open is an agent run. It is NOT
// focusable: the ring simply loses its last station, which nextRegion already knows about, and Tab
// keeps meaning what the hint line says it means.
templ ReadOnlyBar() {
	<div class="flex gap-1 px-1 shrink-0 border-rounded">
		<span class="text-magenta">{"⌾"}</span>
		<span class="font-dim">{"agent run — read only. Ctrl+N starts a chat."}</span>
	</div>
}

// Palette is the command list. The title is the step it is asking about — a picker showing six
// conversation names with no heading is a list with no verb, and Enter on it could mean anything.
//
// It renders the typed filter itself rather than mounting an <input>: a focused Input claims Escape
// (to blur) and every rune in the focus-gated pass, which runs BEFORE the preempt pass the overlay's
// keys ride on — so an Input here would swallow the very keys the palette exists to offer. The same
// reason the approval has no widgets in it either.
templ Palette(title string, query string, entries []paletteEntry, cursor int) {
	<div class="flex-col p-1 border-rounded" width={paletteWidth} borderTitle={title}>
		<div class="flex gap-1">
			<span textStyle={cursorStyle()}>{"›"}</span>
			<span>{query}</span>
			<span textStyle={cursorStyle()}>{"▌"}</span>
		</div>
		<hr />
		if len(entries) == 0 {
			<span class="font-dim">{"nothing matches"}</span>
		}
		for i, e := range entries {
			@PaletteRow(e, i == cursor)
		}
	</div>
}

templ PaletteRow(e paletteEntry, selected bool) {
	<div class="flex gap-1 shrink-0">
		if selected {
			<span textStyle={cursorStyle()}>{"▸"}</span>
		} else {
			<div width={1}></div>
		}
		<span textStyle={rowStyle(selected, false)} width={paletteVerbWidth}>{oneLine(e.Verb, paletteVerbWidth)}</span>
		<span class="font-dim">{oneLine(e.Detail, paletteWidth-paletteVerbWidth-6)}</span>
	</div>
}

// wrap folds text to a width. It is the answer to a long diagnostic, and truncation never is: a
// provider's message puts the reason at the END, so cutting it removes the only part worth reading.
// Greedy, on words, breaking inside a word only when one token is longer than the whole line.
func wrap(s string, width int) []string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s == "" || width <= 0 {
		return nil
	}
	var out []string
	line := make([]rune, 0, width)
	flush := func() {
		if len(line) > 0 {
			out = append(out, string(line))
			line = line[:0]
		}
	}
	for _, word := range strings.Fields(s) {
		w := []rune(word)
		// A word wider than the line is cut across lines rather than left to overflow — a URL with no
		// spaces in it would otherwise push the whole box sideways.
		for len(w) > width {
			flush()
			out = append(out, string(w[:width]))
			w = w[width:]
		}
		switch {
		case len(line) == 0:
			line = append(line, w...)
		case len(line)+1+len(w) <= width:
			line = append(line, ' ')
			line = append(line, w...)
		default:
			flush()
			line = append(line, w...)
		}
	}
	flush()
	return out
}

// toneStyle is what a line MEANS. Four weights rather than one is the difference between a board
// that is scanned and a board that is read — the old view drew a server that was down and the
// fortieth tool name identically.
func toneStyle(t tone) tui.Style {
	switch t {
	case toneBad:
		return tui.NewStyle().Foreground(tui.Red)
	case toneWarn:
		return tui.NewStyle().Foreground(tui.Yellow)
	case toneGood:
		return tui.NewStyle().Foreground(tui.Green)
	case toneDim:
		return tui.NewStyle().Dim()
	default:
		return tui.NewStyle()
	}
}

func sectionTitleStyle() tui.Style { return tui.NewStyle().Foreground(accentHigh).Bold() }

// ProblemBlock is the verdict. It spans the whole page rather than sitting in a column, because its
// reason is the one string this view exists to deliver — and it is wrapped, so nothing is lost.
templ ProblemBlock(p problem, width int) {
	<div class="flex-col shrink-0">
		<div class="flex gap-1">
			if p.Bad {
				<span class="text-red font-bold">{"✗ " + p.Name}</span>
			} else {
				<span class="text-yellow font-bold">{"! " + p.Name}</span>
			}
			<span textStyle={toneStyle(problemTone(p))}>{p.State}</span>
			if p.Where != "" {
				<span class="font-dim">{p.Where}</span>
			}
			<span class="font-dim">{fmt.Sprintf("[%d]", p.Goto)}</span>
		</div>
		for _, line := range p.Reason {
			<div class="flex">
				<div width={2}></div>
				<span textStyle={toneStyle(problemTone(p))}>{line}</span>
			</div>
		}
	</div>
}

func problemTone(p problem) tone {
	if p.Bad {
		return toneBad
	}
	return toneWarn
}

// BoardSection is one box: a digit, a name, a count on the right, and at most four body lines. The
// digit is what opens it — cheaper than a cursor, which would need its own keys and would fight the
// pane's j and k for them.
templ BoardSection(s boardSection, width int, nameWidth int) {
	<div class="flex-col shrink-0" width={width}>
		<div class="flex gap-1">
			<span class="font-dim">{fmt.Sprint(uint8(s.ID))}</span>
			<span textStyle={sectionTitleStyle()}>{s.ID.Title()}</span>
			<div class="grow"></div>
			<span class="font-dim">{s.Count}</span>
		</div>
		for _, l := range s.Body {
			@BoardLine(l, nameWidth, width)
		}
	</div>
}

// BoardLine is a name in a sized column and the rest beside it, so the eye can run down either. The
// gauge takes the name's place where a RATIO is the fact rather than a name.
templ BoardLine(l boardLine, nameWidth int, width int) {
	<div class="flex shrink-0">
		<div width={2}></div>
		if l.Bar != "" {
			<span textStyle={toneStyle(l.Tone)}>{l.Bar}</span>
			<div width={1}></div>
			<span class="font-dim">{oneLine(l.Note, width-nameWidth-3)}</span>
		} else if l.Name != "" {
			<span width={nameWidth}>{oneLine(l.Name, nameWidth)}</span>
			<span textStyle={toneStyle(l.Tone)}>{oneLine(l.Note, width-nameWidth-3)}</span>
		} else {
			<span textStyle={toneStyle(l.Tone)}>{oneLine(l.Note, width-3)}</span>
		}
	</div>
}

// PageBlock is one entry on a section's own page. Its body is already wrapped and is drawn whole:
// this is the one place in the view with no cap of any kind.
templ PageBlock(b pageBlock, width int) {
	<div class="flex-col shrink-0">
		if b.Name != "" {
			<div class="flex gap-1">
				<span textStyle={toneStyle(b.Tone)}>{b.Icon}</span>
				<span class="font-bold">{b.Name}</span>
				<div class="grow"></div>
				<span textStyle={toneStyle(b.Tone)}>{b.State}</span>
			</div>
		}
		if b.Meta != "" {
			<div class="flex">
				<div width={2}></div>
				<span class="font-dim">{oneLine(b.Meta, width-2)}</span>
			</div>
		}
		for _, line := range b.Body {
			<div class="flex">
				<div width={2}></div>
				<span textStyle={toneStyle(b.Tone)}>{line}</span>
			</div>
		}
		if b.Extra != "" {
			<div class="flex">
				<div width={2}></div>
				<span class="font-dim">{oneLine(b.Extra, width-2)}</span>
			</div>
		}
		<div height={1}></div>
	</div>
}

// GridRow is one row of a long flat list. Several to a row because these are short identifiers and a
// single column would turn three hundred files into three hundred lines of mostly blank.
templ GridRow(cells []gridCell, cellWidth int) {
	<div class="flex shrink-0">
		for _, c := range cells {
			<span width={cellWidth}>{oneLine(c.Left, cellWidth-1)}</span>
		}
	</div>
}

// cursorStyle is the ▸ that says "this one". Every list in this UI uses it — the conversations, the
// palette, the approval's options — so that the mark means one thing wherever it appears, in the
// brand's colour rather than in whatever the terminal calls cyan.
func cursorStyle() tui.Style {
	return tui.NewStyle().Foreground(accentHigh).Bold()
}

// rowStyle: the selected row takes the brand's light tint, an unread conversation is bold,
// everything else is plain. Colour says where you are, weight says what changed — two channels for
// two questions.
func rowStyle(selected, unread bool) tui.Style {
	s := tui.NewStyle()
	if selected {
		s = s.Foreground(accentHigh)
	}
	if unread {
		s = s.Bold()
	}
	return s
}

// levelStyle colours a log line by severity. Warnings and errors are what the pane is opened for.
//
// A tui.Style through the textStyle attribute, not a class string. The docs show class={expr} and
// it compiles, but the generator only reads a class attribute that is a STRING LITERAL
// (getClassAttributeValue: "class attribute only supports string literals for now") — anything
// computed is dropped without a word, and the span renders unstyled.
func levelStyle(l slog.Level) tui.Style {
	switch {
	case l >= slog.LevelError:
		return tui.NewStyle().Foreground(tui.Red)
	case l >= slog.LevelWarn:
		return tui.NewStyle().Foreground(tui.Yellow)
	case l >= slog.LevelInfo:
		return tui.NewStyle().Foreground(tui.BrightWhite)
	default:
		return tui.NewStyle().Dim()
	}
}

templ LogLine(l logring.Line) {
	<div class="flex gap-1 shrink-0">
		<span class="font-dim">{l.Time}</span>
		<span textStyle={levelStyle(l.Level)}>{strings.ToUpper(l.Level.String()[:3])}</span>
		if l.Component != "" {
			<span class="text-cyan">{"[" + l.Component + "]"}</span>
		}
		<span>{l.Msg}</span>
		if l.Chat != "" {
			<span class="font-dim">{"chat=" + short(l.Chat)}</span>
		}
		if l.Attrs != "" {
			<span class="font-dim">{l.Attrs}</span>
		}
	</div>
}

// Approval renders the question from the gate's own Action and the options the approver minted —
// never from anything the model wrote, so an injected prompt cannot phrase the question it is being
// asked about.
// The outer element is unconditional: a templ whose whole body sits inside an `if` renders to nil
// when the condition fails, and the framework dereferences what Render returns.
templ Approval(ask *Ask, cursor int) {
	<div class="flex-col gap-1 p-1 border-rounded border-yellow" width={paletteWidth}>
		if ask != nil {
			<span class="font-bold text-yellow">{"approve " + ask.Action.Kind}</span>
			if ask.Action.Target != "" {
				<span class="font-bold">{"→ " + ask.Action.Target}</span>
			}
			<hr />
			for i, o := range ask.Options {
				<div class="flex gap-1">
					if i == cursor {
						<span textStyle={cursorStyle()}>{"▸"}</span>
					} else {
						<div width={1}></div>
					}
					<span class="text-cyan">{fmt.Sprintf("%d", i+1)}</span>
					<span textStyle={rowStyle(i == cursor, false)}>{o.Label}</span>
					if o.Widens {
						<span class="text-yellow">{"(wider than asked)"}</span>
					}
				</div>
			}
			<span class="font-dim">{"↑↓ pick · Enter allow · n or Esc denies"}</span>
		}
	</div>
}

// ToolDetail is one call, whole: what it was given and what it gave back, neither truncated. The
// line in the transcript is a summary and has to be — sixty runes of argument beside an answer being
// read. This is the other half, and it exists because the moment a call is the thing you are asking
// about is the moment the summary is not enough.
//
// Nothing here is styled as code. The values are JSON as often as not, and a syntax highlighter that
// is right half the time is worse than none: it colours the half it understands and leaves the rest
// looking broken.
templ ToolDetail(t transcript.Tool) {
	<div class="flex-col shrink-0">
		<div class="flex gap-1 shrink-0">
			if t.Err != "" {
				<span class="text-red">{"✗ failed"}</span>
			} else if t.Running {
				<span class="text-yellow">{"◐ running"}</span>
			} else {
				<span class="text-green">{"⏺ done"}</span>
			}
			<span class="font-dim">{toolTiming(t, t.Started)}</span>
		</div>
		<hr />
		<span class="font-bold">{"input"}</span>
		if t.Args == "" {
			<span class="font-dim">{"none"}</span>
		} else {
			<span>{t.Args}</span>
		}
		<hr />
		<span class="font-bold">{"output"}</span>
		if t.Err != "" {
			<span class="text-red">{t.Err}</span>
		} else if t.Running {
			// A call still in flight has no result yet. Its sub-agent tail is the only thing there is,
			// and saying so beats an empty box that reads as a result of nothing.
			if t.Stream != "" {
				<span class="font-dim">{t.Stream}</span>
			} else {
				<span class="font-dim">{"still running"}</span>
			}
		} else if t.Result == "" {
			<span class="font-dim">{"none"}</span>
		} else {
			<span>{t.Result}</span>
		}
	</div>
}

// lastRunes keeps the tail of s to at most n runes.
func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// compile-time proof the pure components stay renderable without an App.
var _ = func() tui.Viewable { return ToolLine(transcript.Tool{}, time.Time{}, 80) }
