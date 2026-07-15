package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Messages sent to the program from the turn goroutine / notifier.
type (
	tokenMsg     string
	toolEventMsg tool.Event // one tool call's start/end, from the shared Registry
	doneMsg      struct{ err error }
	pulseMsg     struct{}
	approvalMsg  struct {
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

type approvalPrompt struct {
	intent  string
	options []hitl.Option
	cursor  int
	reply   chan string
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

type chatModel struct {
	startTurn  func(string) context.CancelFunc
	startAgent func(name, task string) context.CancelFunc // run a workspace agent (/<name> <task>)
	agents     []agent.Definition                         // workspace agents, for /agents + /<name> dispatch
	reset      func()                                      // starts a new session: revokes session grants, clears history
	skills     *skill.Index                               // discovered skills, for /name + /skills (never nil)
	markSkill  func(string)                               // mark a /name-activated skill loaded, so skill.load dedups
	model      string                                     // model name, shown in the header

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
	streamMD  string // markdown-rendered, already-committed blocks of the live stream
	streamOff int    // byte offset into stream up to which streamMD has been rendered
	active    map[uint64]*toolFrame
	roots     []uint64
	inputH    int
	pulse     int
	pausedAt  time.Time // when the current approval wait began; zero = not waiting
	running   bool
	cancel    context.CancelFunc
	approval  *approvalPrompt
	notice    string // transient one-line status (e.g. "new session"); cleared on next input
	width     int
	height    int
	ready     bool
}

func newChatModel(startTurn func(string) context.CancelFunc, startAgent func(name, task string) context.CancelFunc, agents []agent.Definition, reset func(), skills *skill.Index, markSkill func(string), model string, dark bool) chatModel {
	ta := textarea.New()
	ta.Placeholder = "Message…"
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")
	ta.SetHeight(1)
	ta.Focus()
	return chatModel{
		startTurn: startTurn, startAgent: startAgent, agents: agents,
		reset: reset, skills: skills, markSkill: markSkill,
		model: model, dark: dark, inputH: 1,
		ta:   ta,
		spin: spinner.New(spinner.WithSpinner(spinner.Dot)),
		sw:   stopwatch.NewWithInterval(100 * time.Millisecond),
		help: help.New(),
		keys: newKeyMap(),
	}
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
	return 3 + len(m.approval.options) // rule + heading + options + hint
}

// growInput sizes the input area to its content (up to maxInputRows).
func (m *chatModel) growInput() {
	rows := strings.Count(m.ta.Value(), "\n") + 1
	if rows < 1 {
		rows = 1
	}
	if rows > maxInputRows {
		rows = maxInputRows
	}
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
	m.stream, m.streamMD, m.streamOff = "", "", 0
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

func (m *chatModel) handleToolEvent(ev tool.Event) {
	switch ev.Phase {
	case tool.Start:
		if m.active == nil {
			m.active = map[uint64]*toolFrame{}
		}
		depth := 0
		if p := m.active[ev.Parent]; p != nil {
			depth = p.depth + 1
		}
		m.active[ev.ID] = &toolFrame{id: ev.ID, parent: ev.Parent, depth: depth, name: ev.Tool, args: ev.Args, started: time.Now()}
		if ev.Parent == 0 {
			m.roots = append(m.roots, ev.ID)
		}
	case tool.End:
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
		m.roots = removeID(m.roots, ev.ID)
		m.entries = append(m.entries, &toolEntry{root: f}) // a finished root commits
	}
}

func removeID(ids []uint64, id uint64) []uint64 {
	for i, x := range ids {
		if x == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
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
	head := toolHeadline(f.name, f.args)
	// No Width() padding here: it would push the trailing reason/duration to the
	// far right edge, away from the tool name. Headlines are already clipped short.
	out := indent + "  " + lead + " " + toolStyle.Render(head) + tail + "\n"
	if body := m.toolBody(f.name, f.args); body != "" {
		out += body
	}

	kids := append([]*toolFrame(nil), f.children...)
	for _, a := range m.active {
		if a.parent == f.id {
			kids = append(kids, a)
		}
	}
	sort.Slice(kids, func(i, j int) bool { return kids[i].id < kids[j].id })
	for _, c := range kids {
		out += m.renderFrame(c, width)
	}
	return out
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

func (m *chatModel) toolBody(name, args string) string {
	if name == "code.run" {
		if src, ok := codeRunSource(args); ok {
			code := m.renderMarkdown("```javascript\n" + strings.TrimSpace(src) + "\n```")
			return strings.TrimRight(code, "\n") + "\n"
		}
	}
	return ""
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
	gap := m.width - visibleWidth(left) - visibleWidth(pill)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + pill + "\n" + m.rule()
}

func (m chatModel) rule() string {
	w := m.width
	if w < 1 {
		w = 1
	}
	return ruleStyle.Render(strings.Repeat("─", w))
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
	b.WriteString(askStyle.Render("Approve ") + m.approval.intent + "\n")
	for i, o := range m.approval.options {
		if i == m.approval.cursor {
			b.WriteString(selectedStyle.Render("▸ " + o.Label))
		} else {
			b.WriteString(hintStyle.Render("  " + o.Label))
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
		wrap := msg.Width - 2 // leave room for the 2-col assistant/tool gutter
		if wrap < 20 {
			wrap = 20
		}
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
			if m.running {
				return m, nil
			}
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
			if m.running {
				return m, nil
			}
			m.reset()
			m.entries = nil
			m.resetStream()
			m.active, m.roots = nil, nil
			m.notice = "new session — grants revoked"
			m.syncViewport()
			return m, nil
		case "esc":
			if m.running && m.cancel != nil {
				m.cancel()
			}
			return m, nil
		case "enter":
			if m.running {
				return m, nil
			}
			input := strings.TrimSpace(m.ta.Value())
			if input == "" {
				return m, nil
			}
			m.ta.Reset()
			m.inputH = 1
			m.ta.SetHeight(1)
			m.notice = ""

			// Slash commands: /skills lists them (no turn); /<name> explicitly
			// activates a skill — the harness injects its body (the model does not
			// take an activation action), and marks it loaded so a later skill.load
			// deduplicates. The transcript shows the typed line; the model gets the
			// expanded input.
			turnInput := input
			if name, ok := strings.CutPrefix(input, "/"); ok {
				cmd, rest, _ := strings.Cut(name, " ")
				if cmd == "skills" {
					m.entries = append(m.entries, &noticeEntry{text: m.skillsListing()})
					m.layout()
					m.syncViewport()
					return m, nil
				}
				if cmd == "agents" {
					m.entries = append(m.entries, &noticeEntry{text: m.agentsListing()})
					m.layout()
					m.syncViewport()
					return m, nil
				}
				// /<name> <task> runs a workspace agent (checked before skills): a fresh
				// epoch + its own grant set + budget + only its tools, streamed like a turn.
				if m.hasAgent(cmd) {
					task := strings.TrimSpace(rest)
					if task == "" {
						task = "Do your task."
					}
					m.ta.Blur()
					m.entries = append(m.entries, &userEntry{text: input})
					m.resetStream()
					m.running = true
					m.cancel = m.startAgent(cmd, task)
					m.layout()
					m.syncViewport()
					return m, tea.Batch(m.sw.Reset(), m.sw.Start())
				}
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
				turnInput = skill.WrapBody(cmd, body) + "\n\n<user_request>\n" + req + "\n</user_request>"
			}

			m.ta.Blur()
			m.entries = append(m.entries, &userEntry{text: input})
			m.resetStream()
			m.running = true
			m.cancel = m.startTurn(turnInput)
			m.layout()
			m.syncViewport()
			return m, tea.Batch(m.sw.Reset(), m.sw.Start()) // start the "thinking" timer
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

	case tokenMsg:
		m.stream += string(msg)
		m.advanceStream()
		m.syncViewport()
		return m, nil

	case toolEventMsg:
		m.handleToolEvent(tool.Event(msg))
		m.syncViewport()
		return m, nil

	case approvalMsg:
		m.approval = &approvalPrompt{intent: msg.intent, options: msg.options, reply: msg.reply}
		m.pausedAt = time.Now() // mark the human wait so tool durations can discount it
		m.layout()
		m.syncViewport()
		return m, m.sw.Stop() // freeze the "thinking" timer while parked on the human

	case doneMsg:
		m.active, m.roots = nil, nil
		m.cancel = nil
		if strings.TrimSpace(m.stream) != "" {
			m.entries = append(m.entries, &assistantEntry{md: m.stream})
		}
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.entries = append(m.entries, &noticeEntry{text: "— cancelled"})
			} else {
				m.entries = append(m.entries, &noticeEntry{text: "error: " + msg.err.Error(), err: true})
			}
		}
		m.resetStream()
		m.running = false
		m.pausedAt = time.Time{}
		m.syncViewport()
		return m, tea.Batch(m.ta.Focus(), m.sw.Stop())
	}

	// Non-key messages (and idle keys while not running) fall through here.
	var cmd tea.Cmd
	if _, isKey := msg.(tea.KeyMsg); isKey {
		if !m.running {
			m.ta, cmd = m.ta.Update(msg)
			m.growInput()
			m.syncViewport()
		}
	} else {
		m.vp, cmd = m.vp.Update(msg)
	}
	return m, cmd
}

func (m chatModel) updateApproval(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.approval.cursor > 0 {
			m.approval.cursor--
		}
	case "down", "j":
		if m.approval.cursor < len(m.approval.options)-1 {
			m.approval.cursor++
		}
	case "enter":
		m.approval.reply <- m.approval.options[m.approval.cursor].Token
		m.approval = nil
		m.resumeAfterApproval() // discount the wait from the tool durations
		m.layout()
		m.syncViewport()
		return m, m.sw.Start() // resume the "thinking" timer: execution continues
	case "esc":
		m.approval.reply <- denyToken(m.approval.options)
		m.approval = nil
		m.resumeAfterApproval()
		if m.cancel != nil {
			m.cancel()
		}
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
