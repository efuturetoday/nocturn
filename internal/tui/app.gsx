package tui

import (
	"context"
	"time"

	tui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/tui/logring"
	"github.com/efuturetoday/nocturn/internal/tui/transcript"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// app is the root component. It owns the workspace wiring, every piece of state two components have
// to agree on, and the keys that mean the same thing everywhere; the panes own their own scroll
// position and their own keys.
//
// The struct is declared HERE rather than in app.go, and that is not a style choice. `tui generate`
// emits BindApp — the method that binds every *tui.State field to the running App — only when it can
// find the struct declaration in the SAME .gsx file (generator_component.go, findStructDecl over the
// file's own decls; the package context it keeps of sibling .go files records names, not fields).
// A component whose struct sits in a .go file silently gets no BindApp, its States stay unbound, and
// State.Set then skips MarkDirty ("construction-time Set before app binding"): the value changes and
// the screen does not.
type app struct {
	// app is the running framework App, assigned by the generated BindApp. Declaring the field is
	// the documented way to get hold of it; nothing here constructs it.
	app *tui.App

	// ctx is the process context, held because firing an agent needs one and the framework's
	// callbacks carry none.
	ctx context.Context
	// ws is nil until the opener finishes. Every path that reaches it goes through ready(), so the
	// UI is usable — and honest about what it cannot do yet — from the first frame.
	ws       *workspace.Workspace
	loaded   <-chan opened
	feed     *Feed
	approver *Approver
	ring     *logring.Ring
	model    string
	// turnStart is when the running turn began, for the context bar's clock. A plain field, not a
	// State: the second-tick that already redraws a running turn is what puts the new number on
	// screen, so nothing that writes it needs to mark a frame dirty.
	turnStart time.Time

	activeID   *tui.State[string]
	activeKind *tui.State[string]
	view       *tui.State[transcript.View]
	chats      *tui.State[[]chat.Meta]
	runs       *tui.State[[]chat.Meta]
	cursor     *tui.State[int]
	draft      *tui.State[string]
	// flash is what just happened, with a deadline on it. Where you are, what is running and what it
	// cost are NOT here — those are derived in contextBar, because they are facts about the state
	// rather than events, and sharing one slot is what made the old status line unreadable.
	flash       *tui.State[flashMsg]
	logOpen     *tui.State[bool]
	inspectOpen *tui.State[bool]
	// filter picks which conversations the sidebar shows. All of them, by default.
	filter *tui.State[listFilter]
	// The workspace view: which page is up, what has been typed into its filter, and whether the
	// keyboard is currently going into that filter.
	inspectSection *tui.State[sectionID]
	inspectFilter  *tui.State[string]
	inspectTyping  *tui.State[bool]
	// The knowledge base by top-level directory, and the file count it was built from. Plain fields
	// and not states: they are rebuilt only when the count changes, because the paths are a disk
	// walk — see refreshInspect.
	docDirs  []boardLine
	docFiles int
	logs     *tui.State[[]logring.Line]
	tokens   *tui.State[int]
	ask      *tui.State[*Ask]
	asking   *tui.State[bool]
	// askCursor is which option the arrow keys have walked to. The digits stay the fastest way to
	// answer; this is the same gesture the list and the palette use, so the question does not have
	// to be read twice to work out how to answer it.
	askCursor *tui.State[int]
	// The palette: whether it is up, what has been typed into it, and which entry is picked. The
	// query is a plain string and not an <input> on purpose — see paletteKeys.
	paletteOpen *tui.State[bool]
	// paletteStep is which of the palette's two questions is up: the verb, or the thing it acts on.
	paletteStep   *tui.State[paletteStep]
	paletteQuery  *tui.State[string]
	paletteCursor *tui.State[int]
	// paletteArmed is the conversation a delete has been chosen for and not yet confirmed. It is a
	// state rather than a flag because the confirmation has to name what it will destroy.
	paletteArmed *tui.State[string]
	// The tool overlay: whether it is up and the call it is showing, copied at the moment it opened.
	toolOpen *tui.State[bool]
	tool     *tui.State[transcript.Tool]
	// toolRefs is where each call's line landed on screen, keyed by turn and call — the only way to
	// tell which one a click hit, since the blocks above it are variable height. go-tui offers no
	// other mechanism: even its own HandleClicks helper is a ref and a ContainsPoint (click.go).
	toolRefs *tui.RefMap[uint64]
	// The overlays' scroll positions live on the ROOT rather than inside their panes, because the
	// root is what handles their wheel — see HandleMouse.
	toolScroll    *tui.State[int]
	inspectScroll *tui.State[int]

	// The panes bind their own elements to these; the root holds them only so it can move the
	// keyboard onto a pane (focusOn) and read the transcript's width.
	side        *tui.Ref
	body        *tui.Ref
	logView     *tui.Ref
	inspectView *tui.Ref
	toolView    *tui.Ref
	composer    *tui.Ref
	// wantFocus is the pane the keyboard is owed, settled after the next frame. See focusOn.
	wantFocus *tui.Ref
}

// opened is the workspace once it exists, or the reason it does not.
type opened struct {
	ws  *workspace.Workspace
	err error
}

func newApp(ctx context.Context, d Deps, loaded <-chan opened) *app {
	return &app{
		ctx:      ctx,
		loaded:   loaded,
		feed:     d.Feed,
		approver: d.Approver,
		ring:     d.Ring,
		model:    d.Model,

		activeID:       tui.NewState(""),
		activeKind:     tui.NewState("user"),
		view:           tui.NewState(transcript.View{}),
		chats:          tui.NewState([]chat.Meta{}),
		runs:           tui.NewState([]chat.Meta{}),
		cursor:         tui.NewState(-1),
		draft:          tui.NewState(""),
		flash:          tui.NewState(flashMsg{}),
		logOpen:        tui.NewState(false),
		inspectOpen:    tui.NewState(false),
		filter:         tui.NewState(filterChats),
		inspectSection: tui.NewState(sectionBoard),
		inspectFilter:  tui.NewState(""),
		inspectTyping:  tui.NewState(false),
		logs:           tui.NewState([]logring.Line{}),
		tokens:         tui.NewState(0),
		ask:            tui.NewState[*Ask](nil),
		asking:         tui.NewState(false),
		askCursor:      tui.NewState(0),
		paletteOpen:    tui.NewState(false),
		paletteStep:    tui.NewState(stepRoot),
		paletteQuery:   tui.NewState(""),
		paletteCursor:  tui.NewState(0),
		paletteArmed:   tui.NewState(""),
		toolOpen:       tui.NewState(false),
		tool:           tui.NewState(transcript.Tool{}),
		toolRefs:       tui.NewRefMap[uint64](),
		toolScroll:     tui.NewState(0),
		inspectScroll:  tui.NewState(0),

		side:        tui.NewRef(),
		body:        tui.NewRef(),
		logView:     tui.NewRef(),
		inspectView: tui.NewRef(),
		toolView:    tui.NewRef(),
		composer:    tui.NewRef(),
	}
}

// The layout: the sidebar and the transcript fill the window, the log pane folds in above the
// composer when it is open, and the approval covers everything while a turn waits on an answer.
// Everything this template calls lives in app.go — a .gsx file holds the shape, not the behaviour.
//
// Every class here is a LITERAL. Tailwind classes are resolved by the generator at build time, so a
// computed class={...} produces an element with no styling at all — which looks exactly like a
// layout bug and never like a dropped attribute. Anything that varies goes through a typed
// attribute instead (width, height, textStyle, borderStyle).
//
// The modal keeps trapFocus at its default. Trapping is what blurs the composer while the question
// is up and restores it afterwards, both without a line from us: opening moves focus into the
// overlay, the dialog holds nothing focusable so focus lands nowhere, and closing puts it back where
// it was. Its catch-all then blocks every key the root has not already claimed.
templ (a *app) Render() {
	// One clock per frame, so every live tool timer in the transcript shows the same second.
	now := time.Now()
	at := a.region()
	// The workspace view, built ONCE for this frame. Both walk the workspace — the knowledge page
	// reads the corpus off disk — and the template needs each of them in three places.
	probs := a.problems()
	page := a.page(a.inspectSection.Get())
	<div class="flex-col h-full w-full">
		<div class="flex grow min-h-0">
			@Sidebar(List{Box: a.side, Focus: a.focusOn, Cursor: a.cursor, ActiveID: a.activeID,
				Filter: a.filter, Rows: a.rows, Sizes: a.listSizes, OnSelect: a.selectRow,
				Focused: at == regList})
			// Keyed on the open chat so switching chats mounts a FRESH transcript pane. A key
			// outside a loop is identity: when it changes the old instance is swept, which is how
			// the pane's scroll position and its follow-the-tail flag reset for a document that has
			// nothing to do with the one that was on screen.
			<div key={a.activeID.Get()} class="flex-col grow min-h-0">
				@ScrollPane(Pane{
					Box: a.body, Focus: a.focusOn, Follow: true,
					Title:   paneTitle(a.bodyTitle(), at == regTranscript),
					Focused: at == regTranscript,
				}) {
					for i, b := range a.view.Get().Blocks {
						@BlockView(b, i, now, a.bodyWidth(), a.toolRefs)
					}
				}
			</div>
		</div>
		if a.logOpen.Get() {
			@ScrollPane(Pane{
				Box: a.logView, Focus: a.focusOn, Height: 12, Follow: true,
				Title:   paneTitle(" logs ", at == regLogs),
				Focused: at == regLogs,
			}) {
				for _, l := range a.logs.Get() {
					@LogLine(l)
				}
			}
		}
		// An agent run has no composer at all. It is a record of what an agent did, with its own
		// persona and its own cage: a message typed into it would be answered by neither. Saying so
		// where the field would be is the honest version of what used to happen — the field took the
		// line, and the refusal arrived afterwards.
		//
		// The replacement is deliberately NOT focusable. Focus is restored by index after every
		// render, so the ring simply loses its last station; nextRegion knows that and the hint line
		// says where Tab goes instead.
		if a.readOnly() {
			@ReadOnlyBar()
		} else {
			// The composer wears the same border as the panes, for the same reason and by a different
			// route: an Input has no borderStyle attribute, only focusColor — the colour its border
			// takes while it holds the keyboard. NewInput leaves that field nil, so without it the
			// field draws identically whether it is focused or not.
			//
			// The unfocused weight is chosen here rather than switched, because border is set once at
			// construction and focusColor is what varies. Square, like every other box.
			<input ref={a.composer} value={a.draft} onSubmit={a.submit} autoFocus width={a.width()}
				placeholder="message, or /help" border={tui.BorderSingle} focusColor={accent} />
		}
		@ContextBar(a.where(), a.activity(), a.tokens.Get(), a.model, a.width())
		<span class="font-dim px-1">{a.hintLine()}</span>
		// Three overlays, at most one of them up: mode() ranks them, and each mode's key table is the
		// only way to answer the one it belongs to. Being modals rather than three different
		// mechanisms is the point — Escape closes, the thing underneath stays where it was, and the
		// keyboard comes back on its own.
		//
		// The approval and the palette hold nothing focusable, which is what keeps the composer from
		// swallowing the keys meant for the question on screen: the trap moves focus into an overlay
		// with no station in it, and the answers ride the root's preempt pass. The workspace view is
		// the opposite case — its pane IS a station, so the trap lands the keyboard on it and its own
		// focus-gated scroll keys work, ahead of the modal's catch-all (the dispatch table runs
		// focus-gated stop handlers in a pass before the preempt pass).
		<modal open={a.asking} closeOnEscape={false} closeOnBackdropClick={false}
			backdrop="dim" class="justify-center items-center">
			@Approval(a.ask.Get(), a.askCursor.Get())
		</modal>
		<modal open={a.paletteOpen} closeOnEscape={false} closeOnBackdropClick={false}
			backdrop="dim" class="justify-center items-center">
			@Palette(a.paletteStep.Get().title(), a.paletteQuery.Get(), a.paletteEntries(), a.paletteCursor.Get())
		</modal>
		// One call, whole. Sized rather than full-screen: what it shows is two blocks of text, and a
		// dialog the width of the window would set them in lines nobody can follow back to the left
		// edge. The pane inside is a real Tab station, so the modal's trap lands the keyboard on it
		// and j/k scroll a long result without a key of ours.
		<modal open={a.toolOpen} closeOnEscape={false} closeOnBackdropClick={false}
			backdrop="dim" class="justify-center items-center">
			// The sized wrapper is what gives the pane its width: ScrollPane takes a height but no
			// width, and a flex column stretches its children across, so the box inside comes out the
			// width asked for here.
			<div class="flex-col" width={toolDetailWidth}>
				@ScrollPane(Pane{
					Box: a.toolView, Focus: a.focusOn, Title: a.toolTitle(),
					Height: toolDetailHeight, Focused: true, Offset: a.toolScroll,
				}) {
					@ToolDetail(a.tool.Get())
				}
			</div>
		</modal>
		// The workspace view takes the whole window: its content is seven groups that only fit side
		// by side, and a card grid squeezed into a centred dialog would be six lines of card and
		// sixty of scrolling.
		<modal open={a.inspectOpen} closeOnEscape={false} closeOnBackdropClick={false}
			backdrop="dim" class="p-1">
			@ScrollPane(Pane{
				Box: a.inspectView, Focus: a.focusOn, Title: a.inspectTitle(),
				Focused: true, Offset: a.inspectScroll,
			}) {
				if a.inspectSection.Get() == sectionBoard {
					// The identity, then the verdict, then the sections. In that order because that
					// is the order the questions are asked in: where am I, is anything wrong, what
					// can it do.
					<div class="flex justify-between shrink-0" width={a.inspectWidth()}>
						<span class="font-bold">{a.workspaceName()}</span>
						<span class="font-dim">{a.capabilitySummary()}</span>
					</div>
					<div height={1}></div>
					if len(probs) > 0 {
						<div class="flex-col shrink-0 px-1 border-thick border-red"
							borderTitle={problemSummary(probs)}>
							for _, p := range probs {
								@ProblemBlock(p, a.inspectWidth()-4)
							}
						</div>
					} else {
						<span class="text-green">{"⏺ all clear — every server up, the vault open"}</span>
					}
					<div height={1}></div>
					// Two columns of sections, dealt DOWN the rows rather than packed by height:
					// where a section lives has to be learned once, and a bin-packer moves it every
					// time the data changes.
					for _, row := range a.boardRows() {
						<div class="flex shrink-0 gap-2">
							for _, s := range row {
								@BoardSection(s, a.boardColWidth(), a.boardNameWidth())
							}
						</div>
						<div height={1}></div>
					}
				} else {
					<span class="font-dim">{page.Summary}</span>
					if a.inspectSection.Get().Filterable() {
						<div class="flex gap-1 shrink-0">
							<span textStyle={cursorStyle()}>{"/"}</span>
							<span>{a.inspectFilter.Get()}</span>
							if a.inspectTyping.Get() {
								<span textStyle={cursorStyle()}>{"▌"}</span>
							}
						</div>
					}
					<hr />
					for _, b := range page.Blocks {
						@PageBlock(b, a.inspectWidth())
					}
					for _, row := range pageGridRows(page) {
						@GridRow(row, a.pageCellWidth(page))
					}
				}
			}
		</modal>
	</div>
}
