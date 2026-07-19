package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/stopwatch"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/skill"
)

// Messages sent to the program: chat events (via the subscription pump) plus a few
// out-of-band lines (scheduler, notify) and the inline-approval fallback.
type (
	// chatEventMsg carries one chat.Event from the OPEN chat — the
	// sole path turns, tokens, tool events and approvals reach the view.
	chatEventMsg struct{ e chat.Event }
	schedulerMsg string // a scheduler firing/skip/result line (background cron runs)
	notifyMsg    string // a notify() fallback line when no out-of-band channel is configured
	pulseMsg     struct{}
	// approvalMsg is the FALLBACK inline approval (tuiNotifier): used only for a run with
	// no attended sink — a background scheduler run when no out-of-band channel is set.
	// Interactive approvals arrive as a chat.ApprovalEvent via chatEventMsg.
	approvalMsg struct {
		intent  string
		options []hitl.Option
		reply   chan string
	}
)

func pulseTick() tea.Cmd {
	return tea.Tick(140*time.Millisecond, func(time.Time) tea.Msg { return pulseMsg{} })
}

const maxInputRows = 6 // the input grows with content up to this many rows

// keyMap drives the bubbles/help footer from real key bindings.
type keyMap struct {
	send, newline, newSession, cancel, scroll, quit key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		send:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "send")),
		newline:    key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("⇧↵", "newline")),
		newSession: key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("⌃N", "new")),
		cancel:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		scroll:     key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("⇞⇟", "scroll")),
		quit:       key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("⌃C", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.send, k.newline, k.newSession, k.scroll, k.quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.send, k.newline}, {k.newSession, k.cancel}, {k.scroll, k.quit}}
}

// approvalPrompt is the on-screen approval, uniform across its two sources: an
// interactive chat.ApprovalEvent (resolve → chat.Resolve) and the inline fallback
// (resolve → reply-token channel). labels are display-only; resolve(choice) enacts the
// pick, choice<0 = deny (fail-closed).
type approvalPrompt struct {
	intent  string
	labels  []string
	cursor  int
	resolve func(choice int)
}

// toolFrame is one tool call in the forest of calls. Calls run concurrently
// (independent roots) and nest (a script's effects inside code.run). children are
// the finished nested effects (a real tree, re-rendered on resize); still-running
// nested effects live in chatModel.active. A frame renders identically live and
// committed — live shows a pulsing dot, committed shows ✓/✗ and its duration.
type toolFrame struct {
	id       uint64
	parent   uint64
	depth    int
	name     string
	args     string
	started  time.Time
	elapsed  time.Duration
	err      error
	done     bool
	children []*toolFrame

	// Display fields derived from args ONCE at Start, so the ~7Hz pulse re-render
	// (renderActive → renderFrame) never re-parses the (possibly large) args JSON.
	head    string // one-line headline, width-independent
	bodySrc string // code.run source to show as a body ("" if none), width-independent
	// bodyCache is the rendered (syntax-highlighted, width-wrapped) body, memoised
	// per width so it re-wraps on resize but not on every pulse.
	bodyCache  string
	bodyCacheW int
	bodyCached bool
}

// entry is one committed item in the transcript. render is width-aware (and may
// cache) so the whole transcript re-wraps when the terminal is resized.
type entry interface {
	render(m *chatModel, width int) string
}

type userEntry struct{ text string }

func (e *userEntry) render(m *chatModel, width int) string {
	return styleWidth(userStyle, width).Render("› " + e.text)
}

type assistantEntry struct {
	md     string // raw markdown, so it re-renders at any width
	cache  string
	cacheW int
	cached bool
}

func (e *assistantEntry) render(m *chatModel, width int) string {
	if !e.cached || e.cacheW != width {
		e.cache = assistantGutter(m.renderMarkdown(e.md))
		e.cacheW, e.cached = width, true
	}
	return e.cache
}

// assistantGutter puts the ◆ marker in a fixed 2-col left gutter so it sits
// beside the first line of the reply — whether that line is prose, a code block,
// a list, or a heading — and continuation lines align under it. JoinHorizontal
// keeps ANSI-styled content intact.
func assistantGutter(block string) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, assistantMark.Render("◆ "), block)
}

type toolEntry struct {
	root   *toolFrame
	cache  string
	cacheW int
	cached bool
}

func (e *toolEntry) render(m *chatModel, width int) string {
	if !e.cached || e.cacheW != width {
		e.cache = strings.TrimRight(m.renderFrame(e.root, width), "\n")
		e.cacheW, e.cached = width, true
	}
	return e.cache
}

type noticeEntry struct {
	text string
	err  bool
}

func (e *noticeEntry) render(m *chatModel, width int) string {
	if e.err {
		return styleWidth(errStyle, width).Render(e.text)
	}
	return hintStyle.Render(e.text)
}

// queuedEntry is an input that arrived while a turn was running (the user typed
// ahead, or a wake fired). It renders DIM with a ⏳ while pending, and flips to a
// normal input line once its turn actually starts (handleChatEvent flips it on the
// chat's TurnStart). wake notes get a ⏰ instead of the ›-prefixed user bubble.
type queuedEntry struct {
	text    string
	pending bool
	wake    bool
}

func (e *queuedEntry) render(m *chatModel, width int) string {
	switch {
	case e.pending && e.wake:
		return hintStyle.Render("⏳ ⏰ " + e.text)
	case e.pending:
		return hintStyle.Render("⏳ " + e.text)
	case e.wake:
		return hintStyle.Render("⏰ resuming: " + e.text)
	default:
		return styleWidth(userStyle, width).Render("› " + e.text)
	}
}

type chatModel struct {
	bw         *bound            // the bound workspace this TUI is driving (its manager/ws/waker)
	workspaces map[string]*bound // all built workspaces, for /ws switching
	chat       *chat.Chat        // the OPEN chat (from bw.chats), driven like the app drives it
	chatID     string            // the open chat's id (in bw's manager)
	openChats  map[string]string // workspace name → its currently-open chat id (remembered across /ws)
	send       func(tea.Msg)     // p.Send — the event pump delivers chat events via this
	unsub      func()            // unsubscribe from the open chat (swapped on /ws + new chat)

	agents []agent.Agent // active workspace's agents, cached on bind (for /agents + /<name>)
	skills *skill.Index  // active workspace's skill catalog, cached on bind (never nil)
	model  string        // model name, shown in the header

	wsNames []string // workspace names, for the /ws listing
	ws      string   // the active workspace name

	vp   viewport.Model
	ta   textarea.Model
	spin spinner.Model
	sw   stopwatch.Model
	help help.Model
	keys keyMap
	md   *glamour.TermRenderer
	dark bool // terminal background, detected once (never re-queried → no escape leak)

	entries   []entry
	stream    string // raw assistant text for the current turn
	thinking  string // live reasoning for the current turn (shown dim, cleared when it ends)
	streamMD  string // markdown-rendered, already-committed blocks of the live stream
	streamOff int    // byte offset into stream up to which streamMD has been rendered
	active    map[uint64]*toolFrame
	roots     []uint64
	inputH    int
	pulse     int
	pausedAt  time.Time      // when the current approval wait began; zero = not waiting
	running   bool           // mirror of the chat loop: set on TurnStart, cleared on TurnEnd
	pending   []*queuedEntry // buffered display entries, FIFO; the front flips live on TurnStart
	approval  *approvalPrompt
	notice    string // transient one-line status (e.g. "new session"); cleared on next input
	width     int
	height    int
	ready     bool
}

func newChatModel(active *bound, workspaces map[string]*bound, names []string, model string, dark bool, send func(tea.Msg)) chatModel {
	ta := textarea.New()
	ta.Placeholder = "Message…"
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")
	ta.SetHeight(1)
	ta.Focus()
	m := chatModel{
		workspaces: workspaces, wsNames: names,
		openChats: map[string]string{},
		send:      send,
		model:     model, dark: dark, inputH: 1,
		ta:   ta,
		spin: spinner.New(spinner.WithSpinner(spinner.Dot)),
		sw:   stopwatch.NewWithInterval(100 * time.Millisecond),
		help: help.New(),
		keys: newKeyMap(),
	}
	m.bindWorkspace(active) // opens the active workspace's first chat and subscribes to it
	return m
}

// bindWorkspace points the model at bw, caches its skill catalog + agent list, and binds its
// currently-open chat — remembered across /ws switches, or a fresh one the first time this
// workspace is bound this chat. Chats live in bw's manager on the process ctx, so
// switching away leaves the chat running (a background wake still fires); switching back
// reattaches and re-syncs from its snapshot.
func (m *chatModel) bindWorkspace(bw *bound) {
	m.bw = bw
	m.agents = bw.ws.Agents()
	m.skills = bw.ws.Skills()
	m.ws = bw.ws.Name()

	id := m.openChats[m.ws]
	r, ok := bw.chats.Open(id)
	if !ok { // first bind this session, or the remembered chat was deleted → a fresh one
		meta, err := bw.chats.New("", chat.OriginUser)
		if err != nil {
			m.notice = "could not open a chat: " + err.Error()
			return
		}
		id = meta.ID
		r, _ = bw.chats.Open(meta.ID)
	}
	m.bindChat(id, r)
}

// bindChat attaches the model to a chat: swap the event pump to it, remember it as
// this workspace's open chat, and rebuild the transcript from its snapshot. Live
// stream/tools/pending are dropped — a mid-turn (re)attach picks up subsequent events.
func (m *chatModel) bindChat(id string, r *chat.Chat) {
	if m.unsub != nil {
		m.unsub() // stop pumping the previously-open chat
	}
	m.chatID = id
	m.openChats[m.ws] = id
	m.chat = r

	sub, unsub := r.Subscribe()
	m.unsub = unsub
	send := m.send
	go func() {
		for e := range sub { // exits when unsub closes the channel
			send(chatEventMsg{e})
		}
	}()

	snap := r.Snapshot()
	m.entries = entriesFromMessages(snap.Messages)
	m.pending = nil
	m.approval = nil
	m.resetStream()
	m.active, m.roots = nil, nil
	m.running = snap.Running
}

// newChat starts a fresh conversation in the active workspace (Ctrl+N): a new chat id, its
// own loop. The previous chat keeps living in the background (its wakes still fire); it is
// abandoned, not reset — an empty one is never persisted (lazy-persist).
func (m *chatModel) newChat() {
	meta, err := m.bw.chats.New("", chat.OriginUser)
	if err != nil {
		m.notice = "could not start a new chat: " + err.Error()
		return
	}
	r, _ := m.bw.chats.Open(meta.ID)
	m.bindChat(meta.ID, r)
}

// entriesFromMessages rebuilds a readable transcript from a chat snapshot's history.
// User turns render as input lines, assistant answers as assistant blocks; system (the
// persona) and tool-plumbing messages are omitted — they are not user-facing. A skill's
// user turn shows its expanded body (that is what the model saw), which is the one
// fidelity trade of reconstructing from history rather than the typed line.
func entriesFromMessages(msgs []brain.Message) []entry {
	var es []entry
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			if strings.TrimSpace(msg.Content) != "" {
				es = append(es, &userEntry{text: msg.Content})
			}
		case "assistant":
			if strings.TrimSpace(msg.Content) != "" {
				es = append(es, &assistantEntry{md: msg.Content})
			}
		}
	}
	return es
}

// markSkill records a /name-activated skill as loaded on the open chat's session so a later
// model skill.load dedups.
func (m *chatModel) markSkill(name string) { m.bw.chats.MarkSkill(m.chatID, name) }

// workspaceListing renders /ws: every built workspace as an invocable /ws <name>,
// the active one marked.
func (m *chatModel) workspaceListing() string {
	var b strings.Builder
	b.WriteString("Workspaces — switch with /ws <name>:\n")
	for _, name := range m.wsNames {
		mark := "  "
		if name == m.ws {
			mark = "▸ "
		}
		fmt.Fprintf(&b, "%s/ws %s\n", mark, name)
	}
	return strings.TrimRight(b.String(), "\n")
}

// resumeAfterApproval shifts every in-flight tool's start forward by the wait it
// just spent at the prompt, so time.Since(started) at ToolEnd excludes it and the
// tool's ✓/✗ duration reflects only real execution. (The "thinking" stopwatch is
// paused/resumed separately via Stop/Start.)
func (m *chatModel) resumeAfterApproval() {
	if m.pausedAt.IsZero() {
		return
	}
	d := time.Since(m.pausedAt)
	for _, f := range m.active {
		f.started = f.started.Add(d)
	}
	m.pausedAt = time.Time{}
}

func (m chatModel) Init() tea.Cmd { return tea.Batch(m.spin.Tick, pulseTick()) }

// glamourStyle is a FIXED style (dark or light, decided once) with zero document
// margin. Using a fixed style means the renderer is rebuilt on resize WITHOUT
// re-querying the terminal background (glamour's WithAutoStyle does query, and its
// OSC response leaks into the input on every resize). Margin 0 lets the transcript
// own its own gutter so markers align.
func glamourStyle(dark bool) ansi.StyleConfig {
	sc := styles.LightStyleConfig
	if dark {
		sc = styles.DarkStyleConfig
	}
	zero := uint(0)
	sc.Document.Margin = &zero
	// Drop the literal "## " / "### " prefixes glamour keeps on headings, so they
	// read as clean styled headings instead of raw hashes.
	sc.H2.Prefix, sc.H3.Prefix, sc.H4.Prefix, sc.H5.Prefix, sc.H6.Prefix = "", "", "", "", ""
	return sc
}

// --- layout -----------------------------------------------------------------

// layout sizes the viewport to whatever the header, status/input (or approval)
// leave. Heights are derived from View's structure, not magic numbers, so the
// approval prompt always reserves exactly the rows it renders.
func (m *chatModel) layout() {
	var h int
	if m.approval != nil {
		h = m.height - m.approvalHeight() - 2 // header(2) + "\n" joins
	} else {
		h = m.height - m.inputH - 3 // header(2) + status(1)
	}
	if h < 3 {
		h = 3
	}
	m.vp.Width, m.vp.Height = m.width, h
	m.ta.SetWidth(m.width)
}

func (m chatModel) approvalHeight() int {
	return 3 + len(m.approval.labels) // rule + heading + options + hint
}

// growInput sizes the input area to its content (up to maxInputRows).
func (m *chatModel) growInput() {
	rows := min(max(strings.Count(m.ta.Value(), "\n")+1, 1), maxInputRows)
	if rows != m.inputH {
		m.inputH = rows
		m.ta.SetHeight(rows)
		m.layout()
	}
}

// skillsListing renders /skills: every discovered skill as an invocable /name
// with its description, plus any discovery diagnostics (skipped/shadowed skills).
func (m *chatModel) skillsListing() string {
	if m.skills.Len() == 0 {
		return "no skills in this workspace (skills/)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "skills (%d):", m.skills.Len())
	for _, s := range m.skills.Skills() {
		fmt.Fprintf(&b, "\n  /%-16s %s", s.Name, s.Description)
	}
	for _, d := range m.skills.Diags {
		fmt.Fprintf(&b, "\n  [%s] %s", d.Level, d.Message)
	}
	return b.String()
}

// hasAgent reports whether name is a defined workspace agent.
func (m chatModel) hasAgent(name string) bool {
	for _, d := range m.agents {
		if d.Name == name {
			return true
		}
	}
	return false
}

// agentsListing renders /agents: every workspace agent as an invocable /<name>
// with its one-line description.
func (m chatModel) agentsListing() string {
	if len(m.agents) == 0 {
		return "No agents. Add one at <ws>/agents/<name>.md (prompt + tools + when)."
	}
	var b strings.Builder
	b.WriteString("Agents (run with /<name> <task>):\n")
	for _, d := range m.agents {
		b.WriteString("  /" + d.Name)
		if d.Description != "" {
			b.WriteString(" — " + d.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- streaming (progressive markdown, kept for live rendering) ---------------

func (m *chatModel) resetStream() {
	m.stream, m.thinking, m.streamMD, m.streamOff = "", "", "", 0
}

func (m *chatModel) advanceStream() {
	rest := m.stream[m.streamOff:]
	split := stableSplit(rest)
	if split == 0 {
		return
	}
	m.streamMD += m.renderMarkdown(rest[:split]) + "\n"
	m.streamOff += split
}

// stableSplit returns the length of the largest prefix of s ending on a blank
// line with all ``` fences closed — the safely-renderable complete blocks.
func stableSplit(s string) int {
	best := 0
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\n' && s[i+1] == '\n' {
			if strings.Count(s[:i], "```")%2 == 0 {
				best = i + 2
			}
		}
	}
	return best
}

// --- tool-call forest --------------------------------------------------------

func (m *chatModel) handleToolEvent(ev activity.ToolEvent) {
	switch ev.Phase {
	case activity.Start:
		if m.active == nil {
			m.active = map[uint64]*toolFrame{}
		}
		depth := 0
		if p := m.active[ev.Parent]; p != nil {
			depth = p.depth + 1
		}
		f := &toolFrame{id: ev.ID, parent: ev.Parent, depth: depth, name: ev.Tool, args: ev.Args, started: time.Now()}
		f.head = toolHeadline(ev.Tool, ev.Args) // parse args once, not on every pulse
		if ev.Tool == "code.run" {
			if src, ok := codeRunSource(ev.Args); ok {
				f.bodySrc = strings.TrimSpace(src)
			}
		}
		m.active[ev.ID] = f
		if ev.Parent == 0 {
			m.roots = append(m.roots, ev.ID)
		}
	case activity.End:
		f := m.active[ev.ID]
		if f == nil {
			return
		}
		delete(m.active, ev.ID)
		f.done, f.err, f.elapsed = true, ev.Err, time.Since(f.started)
		if f.parent != 0 {
			if p := m.active[f.parent]; p != nil {
				p.children = append(p.children, f) // fold into the still-running parent
				return
			}
		}
		m.roots = slices.DeleteFunc(m.roots, func(x uint64) bool { return x == ev.ID })
		m.entries = append(m.entries, &toolEntry{root: f}) // a finished root commits
	}
}

// renderFrame renders a call and its subtree at width. It is used for BOTH live
// (from active) and committed (from a toolEntry) frames: a running frame gets a
// pulsing accent dot; a finished one gets ✓/✗ and its duration. Children are the
// finished sub-frames plus any still-running ones (from active), in id order.
func (m *chatModel) renderFrame(f *toolFrame, width int) string {
	var lead, tail string
	switch {
	case !f.done && m.approval != nil:
		lead = warnStyle.Render("◌") // parked on a human, not running — say so honestly
	case !f.done:
		lead = m.pulseDot()
	case f.err != nil:
		lead = errStyle.Render("✗")
		tail = "  " + hintStyle.Render(shortErr(f.err))
	default:
		lead = okStyle.Render("✓")
		tail = "  " + hintStyle.Render(fmtDuration(f.elapsed))
	}
	indent := strings.Repeat("  ", f.depth)
	var out strings.Builder
	// No Width() padding here: it would push the trailing reason/duration to the
	// far right edge, away from the tool name. Headlines are already clipped short.
	out.WriteString(indent + "  " + lead + " " + toolStyle.Render(f.head) + tail + "\n")
	if f.bodySrc != "" {
		out.WriteString(m.toolBody(f, width))
	}

	kids := append([]*toolFrame(nil), f.children...)
	for _, a := range m.active {
		if a.parent == f.id {
			kids = append(kids, a)
		}
	}
	slices.SortFunc(kids, func(a, b *toolFrame) int { return cmp.Compare(a.id, b.id) })
	for _, c := range kids {
		out.WriteString(m.renderFrame(c, width))
	}
	return out.String()
}

// renderActive renders the still-running forest live (roots in start order).
func (m *chatModel) renderActive(width int) string {
	if len(m.active) == 0 {
		return ""
	}
	var b strings.Builder
	for _, rid := range m.roots {
		if f := m.active[rid]; f != nil {
			b.WriteString(m.renderFrame(f, width))
		}
	}
	return b.String()
}

func (m *chatModel) pulseDot() string {
	return pulseDotStyle(m.pulse).Render("●")
}

// toolHeadline is the one-line label for a tool call — compact and quiet, not
// raw JSON: http.read/write show method + host/path, dns.resolve the host,
// code.run just its name (the JS renders as a body), others a trimmed arg blob.
func toolHeadline(name, args string) string {
	switch name {
	case "http.read", "http.write":
		if u, ok := jsonField(args, "url"); ok {
			return name + " " + clip(compactURL(u), 64)
		}
	case "dns.resolve":
		if h, ok := jsonField(args, "host"); ok {
			return name + " " + clip(h, 64)
		}
	case "code.run":
		return name
	}
	if a := strings.TrimSpace(args); a != "" && a != "{}" {
		return name + " " + clip(a, 64)
	}
	return name
}

// toolBody renders f.bodySrc (a code.run's JS) as highlighted code, memoised per
// width: the syntax highlight is expensive, so the ~7Hz pulse reuses the cached
// render and only recomputes when the terminal width (hence wrap) changes.
func (m *chatModel) toolBody(f *toolFrame, width int) string {
	if !f.bodyCached || f.bodyCacheW != width {
		code := m.renderMarkdown("```javascript\n" + f.bodySrc + "\n```")
		f.bodyCache = strings.TrimRight(code, "\n") + "\n"
		f.bodyCacheW, f.bodyCached = width, true
	}
	return f.bodyCache
}

// --- viewport composition ----------------------------------------------------

func (m *chatModel) syncViewport() {
	wasBottom := m.vp.AtBottom()

	var b strings.Builder
	if len(m.entries) == 0 && !m.running {
		b.WriteString(m.welcome())
	}
	for _, e := range m.entries {
		b.WriteString(e.render(m, m.width))
		b.WriteString("\n\n")
	}
	// Live reasoning (dim, above the tool forest and the answer): shown while the turn
	// thinks, cleared when it ends (resetStream). Wrapped at width so long chains fold.
	if think := strings.TrimSpace(m.thinking); think != "" {
		b.WriteString(styleWidth(hintStyle, m.width).Render("… " + think))
		b.WriteString("\n\n")
	}
	b.WriteString(m.renderActive(m.width))
	live := strings.TrimRight(m.streamMD, "\n")
	if tail := m.stream[m.streamOff:]; strings.TrimSpace(tail) != "" {
		if live != "" {
			live += "\n"
		}
		live += tail
	}
	if live != "" {
		b.WriteString(assistantGutter(live))
	}

	m.vp.SetContent(strings.TrimRight(b.String(), "\n"))
	if wasBottom {
		m.vp.GotoBottom()
	}
}

func (m *chatModel) welcome() string {
	var b strings.Builder
	b.WriteString(welcomeTitle.Render("nocturn ✦") + "\n")
	b.WriteString(welcomeDim.Render("a careful assistant — every effect is gated, approvals arrive out-of-band") + "\n\n")
	b.WriteString(welcomeDim.Render("try") + "\n")
	for _, ex := range []string{
		"resolve the DNS of google.com, wikipedia.org and github.com",
		"fetch example.com and summarize what it says",
		"use a script to compute the 20th Fibonacci number",
	} {
		b.WriteString("  " + hintStyle.Render("› "+ex) + "\n")
	}
	return b.String()
}

// --- chrome ------------------------------------------------------------------

func (m chatModel) header() string {
	left := headerNameStyle.Render("nocturn ✦ ") + headerModelStyle.Render(m.model)
	var pill string
	switch {
	case m.approval != nil:
		pill = warnStyle.Render("● ") + hintStyle.Render("waiting")
	case m.running:
		pill = m.pulseDot() + hintStyle.Render(" thinking")
	default:
		pill = okStyle.Render("● ") + hintStyle.Render("ready")
	}
	gap := max(m.width-visibleWidth(left)-visibleWidth(pill), 1)
	return left + strings.Repeat(" ", gap) + pill + "\n" + m.rule()
}

func (m chatModel) rule() string {
	return ruleStyle.Render(strings.Repeat("─", max(m.width, 1)))
}

func (m chatModel) statusLine() string {
	switch {
	case m.notice != "":
		return hintStyle.Render(m.notice)
	case m.running:
		return m.spin.View() + hintStyle.Render(" thinking "+fmtDuration(m.sw.Elapsed())+" · esc cancel")
	default:
		return m.help.View(m.keys) // idiomatic bubbles/help footer from the key bindings
	}
}

func (m chatModel) approvalView() string {
	var b strings.Builder
	b.WriteString(m.rule() + "\n")
	// The intent is a semantic head plus an optional host-computed fact line
	// ("via <tool> → <cap> @ <host>") joined by a newline; render the head normally
	// and the fact line faint so the human always sees what is really being gated.
	head, fact, hasFact := strings.Cut(m.approval.intent, "\n")
	b.WriteString(askStyle.Render("Approve ") + head + "\n")
	if hasFact {
		b.WriteString(hintStyle.Render(fact) + "\n")
	}
	for i, label := range m.approval.labels {
		if i == m.approval.cursor {
			b.WriteString(selectedStyle.Render("▸ " + label))
		} else {
			b.WriteString(hintStyle.Render("  " + label))
		}
		b.WriteString("\n")
	}
	b.WriteString(hintStyle.Render("↑↓ select · ↵ confirm · esc deny"))
	return b.String()
}

func (m chatModel) View() string {
	if !m.ready {
		return ""
	}
	if m.approval != nil {
		return m.header() + "\n" + m.vp.View() + "\n" + m.approvalView()
	}
	return m.header() + "\n" + m.vp.View() + "\n" + m.statusLine() + "\n" + m.ta.View()
}

func (m chatModel) renderMarkdown(s string) string {
	if m.md != nil {
		if out, err := m.md.Render(s); err == nil {
			return strings.Trim(out, "\n") // glamour pads with blank lines; drop them so markers/code tuck in
		}
	}
	return s
}

// --- update ------------------------------------------------------------------

// handleSlash dispatches a /command: immediate UI listings (/skills, /agents, /ws),
// a workspace switch, or a skill/agent activation that is SUBMITTED to the chat (so it
// streams + gates like a chat turn). Skill/agent submits carry the typed line as the
// display and the expanded body/task as the payload — the transcript shows "/name …", the
// model gets the real input. Called only when not mid-turn (Update guards that).
func (m chatModel) handleSlash(input string) (tea.Model, tea.Cmd) {
	name, _ := strings.CutPrefix(input, "/")
	cmd, rest, _ := strings.Cut(name, " ")
	switch cmd {
	case "skills":
		m.entries = append(m.entries, &noticeEntry{text: m.skillsListing()})
		m.layout()
		m.syncViewport()
		return m, nil
	case "agents":
		m.entries = append(m.entries, &noticeEntry{text: m.agentsListing()})
		m.layout()
		m.syncViewport()
		return m, nil
	case "ws":
		// /ws lists workspaces; /ws <name> switches the active one — rebinds this model to
		// that workspace's isolated stack (its chats/agents/skills) and rebuilds the visible
		// transcript from that chat's snapshot (bindWorkspace).
		if target := strings.TrimSpace(rest); target == "" {
			m.entries = append(m.entries, &noticeEntry{text: m.workspaceListing()})
		} else if st, ok := m.workspaces[target]; ok {
			m.bindWorkspace(st) // rebuilds entries/running from st's snapshot
			m.notice = "workspace: " + target
		} else {
			m.entries = append(m.entries, &noticeEntry{text: "unknown workspace: /ws " + target + " (try /ws)", err: true})
		}
		m.layout()
		m.syncViewport()
		return m, nil
	}
	// /<name> <task> runs a workspace agent (checked before skills): submitted as an agent
	// turn (fresh epoch + its own grants + only its tools), streamed like a chat turn.
	if m.hasAgent(cmd) {
		task := strings.TrimSpace(rest)
		if task == "" {
			task = "Do your task."
		}
		m.chat.SubmitAgent(input, cmd, task)
		return m, nil
	}
	// /<name> activates a skill: the harness injects its body (the model takes no
	// activation action) and marks it loaded so a later model skill.load dedups.
	s, found := m.skills.Get(cmd)
	if !found {
		m.entries = append(m.entries, &noticeEntry{text: "unknown skill: /" + cmd + " (try /skills)", err: true})
		m.layout()
		m.syncViewport()
		return m, nil
	}
	body, err := s.Body()
	if err != nil {
		m.entries = append(m.entries, &noticeEntry{text: "skill " + cmd + ": " + err.Error(), err: true})
		m.layout()
		m.syncViewport()
		return m, nil
	}
	m.markSkill(cmd)
	req := strings.TrimSpace(rest)
	if req == "" {
		req = "Follow the skill's instructions."
	}
	m.chat.SubmitInput(chat.SourceUser, input, skill.WrapBody(cmd, body)+"\n\n<user_request>\n"+req+"\n</user_request>", "")
	return m, nil
}

// scrollKeys let the user read back during a turn without the view snapping to
// the bottom (syncViewport only auto-scrolls when already at the bottom).
var scrollKeys = map[string]bool{
	"pgup": true, "pgdown": true, "ctrl+u": true, "ctrl+d": true, "home": true, "end": true,
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !m.ready {
			m.vp = viewport.New(msg.Width, 3)
			m.ready = true
		}
		wrap := max(
			// leave room for the 2-col assistant/tool gutter
			msg.Width-2, 20)
		m.md, _ = glamour.NewTermRenderer(glamour.WithStyles(glamourStyle(m.dark)), glamour.WithWordWrap(wrap))
		m.help.Width = msg.Width
		m.layout()
		m.syncViewport()
		return m, nil

	case tea.MouseMsg:
		// Wheel scrolls the transcript; every mouse event is consumed here so none
		// can fall through to the input.
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		if isMouseLeak(msg) {
			return m, nil // a stray SGR mouse report (edge scrolling); drop it
		}
		if m.approval != nil {
			return m.updateApproval(msg)
		}
		if scrollKeys[msg.String()] {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		if msg.Paste {
			var cmd tea.Cmd
			m.ta, cmd = m.ta.Update(msg)
			m.growInput()
			m.syncViewport()
			return m, cmd
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+n":
			// Start a fresh chat. The previous one keeps running in the background (its turn
			// finishes, its wakes still fire) — it is abandoned, not reset, so this is allowed
			// even mid-turn. bindChat rebuilds the view from the new (empty) chat.
			m.newChat()
			m.notice = "new chat"
			m.syncViewport()
			return m, nil
		case "esc":
			m.chat.Cancel() // stops a running turn; a no-op when idle
			return m, nil
		case "enter":
			input := strings.TrimSpace(m.ta.Value())
			if input == "" {
				return m, nil
			}
			// Slash commands are immediate UI actions (or skill/agent submits) and are
			// ignored mid-turn; a plain message is submitted to the chat, which serializes
			// it — buffering behind a running turn itself — and streams back the events that
			// drive the transcript. The TUI no longer sequences turns or holds a queue.
			if m.running && strings.HasPrefix(input, "/") {
				return m, nil
			}
			m.ta.Reset()
			m.inputH = 1
			m.ta.SetHeight(1)
			m.notice = ""
			if strings.HasPrefix(input, "/") {
				return m.handleSlash(input)
			}
			m.chat.Submit(chat.SourceUser, input, "")
			return m, nil
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case stopwatch.TickMsg, stopwatch.StartStopMsg, stopwatch.ResetMsg:
		var cmd tea.Cmd
		m.sw, cmd = m.sw.Update(msg)
		return m, cmd

	case pulseMsg:
		m.pulse++
		if m.running || len(m.active) > 0 {
			m.syncViewport()
		}
		return m, pulseTick()

	case chatEventMsg:
		return m.handleChatEvent(msg.e)

	case schedulerMsg:
		// A background cron run announced itself (firing/skip/done). Show it as a
		// dim system line so it never masquerades as the interactive assistant.
		m.entries = append(m.entries, &noticeEntry{text: string(msg)})
		m.syncViewport()
		return m, nil

	case notifyMsg:
		// notify() with no out-of-band channel configured — surface it inline as a
		// dim system line (the attended fallback for a phone push).
		m.entries = append(m.entries, &noticeEntry{text: "🔔 " + string(msg)})
		m.syncViewport()
		return m, nil

	case approvalMsg:
		// FALLBACK inline approval (tuiNotifier): a run with no attended sink. resolve
		// writes the chosen (or deny) token back on the reply channel — deny on choice<0.
		options, reply := msg.options, msg.reply
		labels := make([]string, len(options))
		for i, o := range options {
			labels[i] = o.Label
		}
		m.approval = &approvalPrompt{
			intent: msg.intent,
			labels: labels,
			resolve: func(choice int) {
				if choice >= 0 && choice < len(options) {
					reply <- options[choice].Token
					return
				}
				reply <- denyToken(options)
			},
		}
		m.pausedAt = time.Now() // mark the human wait so tool durations can discount it
		m.layout()
		m.syncViewport()
		return m, m.sw.Stop() // freeze the "thinking" timer while parked on the human
	}

	// A key not handled above goes to the textarea — even while a turn runs, so the
	// user can type ahead (Enter then buffers it; see the enter case).
	var cmd tea.Cmd
	if _, isKey := msg.(tea.KeyMsg); isKey {
		m.ta, cmd = m.ta.Update(msg)
		m.growInput()
		m.syncViewport()
	} else {
		m.vp, cmd = m.vp.Update(msg)
	}
	return m, cmd
}

// handleChatEvent renders one event from the open chat — the sole
// path turns, tokens, tool calls and approvals reach the view. The chat loop owns turn
// SEQUENCING (serialize + queue); the TUI owns only DISPLAY, driven from these events in
// the loop's order (so no keystroke-vs-event race). User/agent turns append their
// display line on TurnStart; a wake/remind is created here too (it has no keystroke).
func (m chatModel) handleChatEvent(e chat.Event) (tea.Model, tea.Cmd) {
	switch ev := e.(type) {
	case chat.TurnStartEvent:
		m.running = true
		m.resetStream()
		m.active, m.roots = nil, nil
		if len(m.pending) > 0 {
			// This turn was buffered: its pending display entry flips live (FIFO — the
			// loop drains its queue front-first, in the same order these were emitted).
			p := m.pending[0]
			m.pending = m.pending[1:]
			p.pending = false
		} else {
			m.entries = append(m.entries, m.liveEntry(ev.Display, ev.Source))
		}
		m.layout()
		m.syncViewport()
		return m, tea.Batch(m.sw.Reset(), m.sw.Start())

	case chat.QueuedEvent:
		qe := &queuedEntry{text: ev.Display, pending: true, wake: isWakeSource(ev.Source)}
		m.entries = append(m.entries, qe)
		m.pending = append(m.pending, qe)
		m.layout()
		m.syncViewport()
		return m, nil

	case chat.TokenEvent:
		m.stream += ev.Text
		m.advanceStream()
		m.syncViewport()
		return m, nil

	case chat.ThinkingEvent:
		m.thinking += ev.Text
		m.syncViewport()
		return m, nil

	case chat.ToolEvent:
		m.handleToolEvent(ev.Event)
		m.syncViewport()
		return m, nil

	case chat.TurnEndEvent:
		m.active, m.roots = nil, nil
		if strings.TrimSpace(m.stream) != "" {
			m.entries = append(m.entries, &assistantEntry{md: m.stream})
		}
		if ev.Err != nil {
			if errors.Is(ev.Err, context.Canceled) {
				m.entries = append(m.entries, &noticeEntry{text: "— cancelled"})
			} else {
				m.entries = append(m.entries, &noticeEntry{text: "error: " + ev.Err.Error(), err: true})
			}
		}
		m.resetStream()
		m.running = false
		m.pausedAt = time.Time{}
		m.syncViewport()
		// The chat auto-starts the next queued turn (another TurnStart) — no drain here.
		return m, tea.Batch(m.ta.Focus(), m.sw.Stop())

	case chat.NoticeEvent:
		m.entries = append(m.entries, &noticeEntry{text: ev.Text, err: ev.Err})
		m.syncViewport()
		return m, nil

	case chat.ApprovalEvent:
		// Interactive (attended) approval on this session's stream. resolve routes to the
		// chat; a deny (choice<0, from Esc) also cancels the turn — Esc means "stop".
		r, id := m.chat, ev.ID
		m.approval = &approvalPrompt{
			intent: ev.Intent,
			labels: ev.Options,
			resolve: func(choice int) {
				if choice < 0 {
					r.Resolve(id, -1)
					r.Cancel()
					return
				}
				r.Resolve(id, choice)
			},
		}
		m.pausedAt = time.Now()
		m.layout()
		m.syncViewport()
		return m, m.sw.Stop()

	case chat.ApprovalResolvedEvent:
		// The pending approval was answered (possibly out of band, on another device) —
		// clear the prompt if it is still up.
		if m.approval != nil {
			m.approval = nil
			m.resumeAfterApproval()
			m.layout()
			m.syncViewport()
			return m, m.sw.Start()
		}
		return m, nil
	}
	return m, nil
}

// liveEntry is the transcript line for a turn that starts immediately (not buffered): a
// user turn or in-chat agent spawn shows its typed line; a wake/remind shows a
// "⏰ resuming" line; a cron firing (seen when watching a fired agent chat) shows a
// dim scheduled-task line.
func (m *chatModel) liveEntry(display string, src chat.Source) entry {
	switch {
	case isWakeSource(src):
		return &queuedEntry{text: display, wake: true}
	case src == chat.SourceSchedule:
		return &noticeEntry{text: "⏱ " + display}
	default:
		return &userEntry{text: display}
	}
}

func isWakeSource(s chat.Source) bool {
	return s == chat.SourceWake || s == chat.SourceRemind
}

func (m chatModel) updateApproval(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.approval.cursor > 0 {
			m.approval.cursor--
		}
	case "down", "j":
		if m.approval.cursor < len(m.approval.labels)-1 {
			m.approval.cursor++
		}
	case "enter":
		m.approval.resolve(m.approval.cursor)
		m.approval = nil
		m.resumeAfterApproval() // discount the wait from the tool durations
		m.layout()
		m.syncViewport()
		return m, m.sw.Start() // resume the "thinking" timer: execution continues
	case "esc":
		m.approval.resolve(-1) // deny (fail-closed); the interactive path also cancels the turn
		m.approval = nil
		m.resumeAfterApproval()
		m.layout()
		m.syncViewport()
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

// --- small helpers -----------------------------------------------------------

// isMouseLeak reports whether a key event is actually a stray SGR mouse report
// that slipped past the parser — some terminals emit "[<Cb;Cx;Cy M/m" when the
// wheel is turned at the scroll edge. Dropping it keeps mouse noise out of the input.
func isMouseLeak(k tea.KeyMsg) bool {
	s := k.String()
	return strings.Contains(s, "[<") && (strings.HasSuffix(s, "M") || strings.HasSuffix(s, "m"))
}

func styleWidth(s lipgloss.Style, w int) lipgloss.Style {
	if w > 0 {
		return s.Width(w)
	}
	return s
}

func pulseDotStyle(pulse int) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(pulsePalette[pulse%len(pulsePalette)]))
}

func visibleWidth(s string) int { return lipgloss.Width(s) }

// codeRunSource pulls the JS source out of a code.run call's JSON args so the
// TUI can show it as readable, highlighted code instead of a crammed one-liner.
func codeRunSource(args string) (string, bool) {
	if src, ok := jsonField(args, "source"); ok {
		return src, true
	}
	return "", false
}

func jsonField(args, key string) (string, bool) {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return v, true
		}
	}
	return "", false
}

func compactURL(u string) string {
	return strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func shortErr(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		s := err.Error()
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[:i] // collapse a multi-line guest/JS stack to its first line
		}
		return clip(s, 80)
	}
}

func denyToken(options []hitl.Option) string {
	for _, o := range options {
		if o.Outcome == hitl.Denied {
			return o.Token
		}
	}
	return ""
}
