package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/llm"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
)

var (
	userStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	toolStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	askStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	hintStyle     = lipgloss.NewStyle().Faint(true)
)

// pulsePalette runs dim -> bright cyan -> dim, for the glowing tool-call dot.
var pulsePalette = []string{"23", "30", "37", "44", "51", "44", "37", "30"}

// Messages sent to the program from the turn goroutine / notifier.
type (
	tokenMsg     string
	toolEventMsg brain.ToolEvent // one tool call's start/end, from the shared Registry
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

type approval struct {
	intent  string
	options []hitl.Option
	cursor  int
	reply   chan string
}

// toolFrame is one in-flight tool call on the call stack. A script's effects
// nest inside code.run, so calls form a stack: children accumulate into their
// parent's rendered lines and are committed together when the parent ends.
type toolFrame struct {
	name     string
	args     string
	children []string // rendered, already-committed child lines (nested effects)
}

type chatModel struct {
	startTurn func(string) context.CancelFunc
	reset     func() // starts a new session: revokes session grants, clears history

	vp   viewport.Model
	ta   textarea.Model
	spin spinner.Model
	md   *glamour.TermRenderer

	history   string
	stream    string      // raw accumulated assistant text for the current turn
	streamMD  string      // markdown-rendered, already-committed blocks of stream
	streamOff int         // byte offset into stream up to which streamMD has been rendered
	callStack []toolFrame // in-flight tool calls (pulsing); empty when none
	pulse     int
	running    bool
	cancel     context.CancelFunc // cancels the current turn
	approval   *approval
	width      int
	height     int
	ready      bool
}

func newChatModel(startTurn func(string) context.CancelFunc, reset func()) chatModel {
	ta := textarea.New()
	ta.Placeholder = "Message…"
	ta.Prompt = "❯ "
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")
	ta.SetHeight(1)
	ta.Focus()
	return chatModel{startTurn: startTurn, reset: reset, ta: ta, spin: spinner.New(spinner.WithSpinner(spinner.Dot))}
}

func (m chatModel) Init() tea.Cmd { return tea.Batch(m.spin.Tick, pulseTick()) }

func (m *chatModel) layout() {
	bottom := 2
	if m.approval != nil {
		bottom = 1 + len(m.approval.options)
	}
	h := m.height - bottom - 1
	if h < 3 {
		h = 3
	}
	m.vp.Width, m.vp.Height = m.width, h
	m.ta.SetWidth(m.width)
}

// resetStream clears the per-turn streaming state (raw text + rendered blocks).
func (m *chatModel) resetStream() {
	m.stream = ""
	m.streamMD = ""
	m.streamOff = 0
}

// toolDisplay splits a tool call into a one-line headline and an optional
// multi-line body. code.run's JS source becomes a rendered ```javascript block;
// every other tool shows its raw args inline.
func (m chatModel) toolDisplay(name, args string) (headline, body string) {
	if name == "code.run" {
		if src, ok := codeRunSource(args); ok {
			code := m.renderMarkdown("```javascript\n" + strings.TrimSpace(src) + "\n```")
			return name, strings.TrimRight(code, "\n") + "\n"
		}
	}
	return name + "(" + args + ")", ""
}

// renderToolFrame renders a completed tool call for the static transcript: a
// bullet at depth 0, a nested "↳" child at depth ≥1, the headline (wrapped),
// code.run's JS body, and any child lines it accumulated. On error the line is
// red with the reason underneath.
func (m chatModel) renderToolFrame(f toolFrame, err error, depth int) string {
	style := toolStyle
	if err != nil {
		style = errStyle
	}
	bullet := "  ● "
	if depth > 0 {
		bullet = "    ↳ "
	}
	headline, body := m.toolDisplay(f.name, f.args)
	out := styleWidth(style, m.width).Render(bullet+headline) + "\n" + body
	for _, c := range f.children {
		out += c
	}
	if err != nil {
		out += styleWidth(errStyle, m.width).Render("      ↳ "+shortErr(err)) + "\n"
	}
	return out
}

// handleToolEvent tracks the tool call stack: a ToolStart pushes a frame, a
// ToolEnd pops it and renders it — nested effects fold into their parent's child
// lines, a top-level call commits straight to the transcript.
func (m *chatModel) handleToolEvent(ev brain.ToolEvent) {
	switch ev.Phase {
	case brain.ToolStart:
		m.callStack = append(m.callStack, toolFrame{name: ev.Tool, args: ev.Args})
	case brain.ToolEnd:
		if len(m.callStack) == 0 {
			return
		}
		frame := m.callStack[len(m.callStack)-1]
		m.callStack = m.callStack[:len(m.callStack)-1]
		depth := len(m.callStack) // the popped frame's own depth
		block := m.renderToolFrame(frame, ev.Err, depth)
		if depth > 0 {
			parent := &m.callStack[len(m.callStack)-1]
			parent.children = append(parent.children, block)
		} else {
			m.history += block
		}
	}
}

// renderActiveStack renders the in-flight call stack live: the top-level call
// pulses with its already-completed children beneath it, and any nested effect
// currently running is shown as its own pulsing line.
func (m chatModel) renderActiveStack() string {
	if len(m.callStack) == 0 {
		return ""
	}
	dot := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(pulsePalette[m.pulse%len(pulsePalette)])).Render("●")
	top := m.callStack[0]
	headline, body := m.toolDisplay(top.name, top.args)
	// Dot + space sit outside the wrapped style (4 visible columns), so wrap the
	// headline to width-4 to keep padding from overflowing the line.
	out := "  " + dot + " " + styleWidth(toolStyle, m.width-4).Render(headline) + "\n" + body
	for _, c := range top.children {
		out += c
	}
	for _, f := range m.callStack[1:] { // a nested effect in progress
		hl, _ := m.toolDisplay(f.name, f.args)
		out += "    " + dot + " " + styleWidth(toolStyle, m.width-6).Render(hl) + "\n"
	}
	return out
}

func (m *chatModel) syncViewport() {
	content := m.history + m.renderActiveStack()
	content += m.streamMD
	if tail := m.stream[m.streamOff:]; strings.TrimSpace(tail) != "" {
		content += tail // still-incomplete last block, shown raw until it closes
	}
	m.vp.SetContent(content)
	m.vp.GotoBottom()
}

// advanceStream renders any newly-completed markdown blocks from the raw stream
// into streamMD, leaving the trailing incomplete block raw. A block ends at a
// blank line; we never split inside an open ``` code fence.
func (m *chatModel) advanceStream() {
	rest := m.stream[m.streamOff:]
	split := stableSplit(rest)
	if split == 0 {
		return
	}
	m.streamMD += m.renderMarkdown(rest[:split]) + "\n"
	m.streamOff += split
}

// stableSplit returns the byte length of the largest prefix of s that ends on a
// blank-line boundary with all ``` code fences closed — i.e. only complete,
// safely-renderable top-level blocks. Returns 0 if nothing is complete yet.
func stableSplit(s string) int {
	best := 0
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\n' && s[i+1] == '\n' {
			if strings.Count(s[:i], "```")%2 == 0 {
				best = i + 2 // consume both newlines
			}
		}
	}
	return best
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !m.ready {
			m.vp = viewport.New(msg.Width, 3)
			m.ready = true
		}
		m.md, _ = glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(msg.Width))
		m.layout()
		m.syncViewport()
		return m, nil

	case tea.KeyMsg:
		if m.approval != nil {
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
				m.layout()
				m.syncViewport()
			case "esc":
				m.approval.reply <- denyToken(m.approval.options)
				m.approval = nil
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
		if msg.Paste {
			if m.running {
				return m, nil
			}
			var cmd tea.Cmd
			m.ta, cmd = m.ta.Update(msg)
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
			m.history = ""
			m.resetStream()
			m.callStack = nil
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
			m.ta.Blur()
			m.history += styleWidth(userStyle, m.width).Render("❯ "+input) + "\n\n"
			m.resetStream()
			m.running = true
			m.cancel = m.startTurn(input)
			m.syncViewport()
			return m, nil
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case pulseMsg:
		m.pulse++
		if len(m.callStack) > 0 {
			m.syncViewport()
		}
		return m, pulseTick()

	case tokenMsg:
		m.stream += string(msg)
		m.advanceStream()
		m.syncViewport()
		return m, nil

	case toolEventMsg:
		m.handleToolEvent(brain.ToolEvent(msg))
		m.syncViewport()
		return m, nil

	case approvalMsg:
		m.approval = &approval{intent: msg.intent, options: msg.options, reply: msg.reply}
		m.layout()
		m.syncViewport()
		return m, nil

	case doneMsg:
		m.callStack = nil // every ToolStart is paired with a ToolEnd; clear defensively
		m.cancel = nil
		full := m.streamMD
		if tail := m.stream[m.streamOff:]; strings.TrimSpace(tail) != "" {
			full += m.renderMarkdown(tail)
		}
		if strings.TrimSpace(full) != "" {
			m.history += strings.TrimRight(full, "\n") + "\n\n"
		}
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.history += hintStyle.Render("— cancelled") + "\n\n"
			} else {
				m.history += styleWidth(errStyle, m.width).Render("error: "+msg.err.Error()) + "\n\n"
			}
		}
		m.resetStream()
		m.running = false
		m.syncViewport()
		return m, m.ta.Focus()
	}

	var cmd tea.Cmd
	if _, isKey := msg.(tea.KeyMsg); isKey {
		if !m.running && m.approval == nil {
			m.ta, cmd = m.ta.Update(msg)
		}
	} else {
		m.vp, cmd = m.vp.Update(msg)
	}
	return m, cmd
}

func (m chatModel) renderMarkdown(s string) string {
	if m.md != nil {
		if out, err := m.md.Render(s); err == nil {
			return strings.TrimRight(out, "\n")
		}
	}
	return s
}

func (m chatModel) View() string {
	if !m.ready {
		return "starting…"
	}
	if m.approval != nil {
		b := m.vp.View() + "\n" + askStyle.Render("Approve: ") + m.approval.intent + "\n"
		for i, o := range m.approval.options {
			cursor, line := "  ", o.Label
			if i == m.approval.cursor {
				cursor, line = "▸ ", selectedStyle.Render(o.Label)
			}
			b += cursor + line + "\n"
		}
		b += hintStyle.Render("↑↓ select · Enter · Esc = deny")
		return b
	}

	var status string
	if m.running {
		status = m.spin.View() + hintStyle.Render(" thinking…   Esc cancel · ctrl+c quit")
	} else {
		status = hintStyle.Render("Enter send · Ctrl+J newline · Ctrl+N new session · ctrl+c quit")
	}
	return m.vp.View() + "\n" + status + "\n" + m.ta.View()
}

// styleWidth constrains a lipgloss style to w columns, which enables word-wrap;
// with a non-positive width (before the first WindowSizeMsg) it is a no-op.
func styleWidth(s lipgloss.Style, w int) lipgloss.Style {
	if w > 0 {
		return s.Width(w)
	}
	return s
}

// codeRunSource pulls the JS source out of a code.run call's JSON args so the
// TUI can show it as readable, syntax-highlighted code (unmarshalling turns the
// escaped \n back into real newlines) instead of a crammed one-line string.
func codeRunSource(args string) (string, bool) {
	var a struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(args), &a); err == nil && strings.TrimSpace(a.Source) != "" {
		return a.Source, true
	}
	return "", false
}

func shortErr(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return err.Error()
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

// tuiNotifier bridges HITL approval into the TUI.
type tuiNotifier struct {
	p       *tea.Program
	resolve func(token string) error
}

func (n *tuiNotifier) Notify(intent string, options []hitl.Option) error {
	reply := make(chan string)
	n.p.Send(approvalMsg{intent: intent, options: options, reply: reply})
	return n.resolve(<-reply)
}

func tuiCmd(_ []string) error {
	_ = godotenv.Load()
	baseURL, apiKey := os.Getenv("FREELLM_BASE_URL"), os.Getenv("FREELLM_API_KEY")
	modelName := os.Getenv("FREELLM_MODEL")
	if apiKey == "" {
		return errors.New("FREELLM_API_KEY not set (see .env / .env.example)")
	}
	if modelName == "" {
		modelName = "auto"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	notifier := &tuiNotifier{}
	engine := hitl.NewEngine(key, notifier)
	notifier.resolve = engine.Resolve

	epochs := capability.NewEpochRegistry()
	store := secret.NewStore()
	netCap := &gateway.Net{
		Guard: &gateway.Guard{
			Policy: capability.Policy{Rules: []capability.Rule{
				{Capability: "http.read", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
				{Capability: "http.write", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
				{Capability: "dns.resolve", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
			}},
			Approvals: engine,
			Epochs:    epochs, // shared with the session, so "Allow this session" grants are revocable
			TTL:       2 * time.Minute,
		},
		Scanner: secret.NewScanner(store),
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
	// One shared Registry dispatches every tool call — the model's AND the
	// script's — so its OnCall observer sees them all in one place, nested by
	// call order.
	reg := brain.NewRegistry(netCap.Tools())

	// A script interpreter (QuickJS on the sandbox) exposed as code.run: the
	// model can run multi-step JS that reaches effects via one generic host gate
	// (nocturn.call), dispatched back through the SAME Registry — every effect
	// still passes Guard.Authorize + out-of-band HITL. Pure compute needs no
	// approval. The Timeout is a backstop; the brain's ToolTimeout (via ctx)
	// governs normally. code.run is added after the runner exists so the
	// interpreter can dispatch back into the Registry; a script may not re-enter
	// it (the runner's dispatch refuses code.run).
	runner := script.NewRunner(reg)
	runner.Timeout = 60 * time.Second
	reg.Add(runner.Tool())

	var p *tea.Program
	reg.OnCall = func(ev brain.ToolEvent) { p.Send(toolEventMsg(ev)) }
	b := &brain.Brain{
		Model:       llm.New(baseURL, apiKey, modelName),
		Registry:    reg,
		ToolTimeout: 20 * time.Second,
		OnToken:     func(tok string) { p.Send(tokenMsg(tok)) },
	}
	session := agent.New(b, netCap.Guard, epochs)
	defer session.Close()
	startTurn := func(input string) context.CancelFunc {
		turnCtx, cancel := context.WithCancel(ctx)
		go func() {
			_, err := session.Ask(turnCtx, input)
			p.Send(doneMsg{err: err})
		}()
		return cancel
	}

	p = tea.NewProgram(newChatModel(startTurn, session.Reset), tea.WithAltScreen())
	notifier.p = p
	_, err := p.Run()
	return err
}
