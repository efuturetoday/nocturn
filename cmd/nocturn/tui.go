package main

import (
	"context"
	"crypto/rand"
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
	tokenMsg      string
	toolMsg       struct{ name, args string }
	toolResultMsg struct{ err error }
	doneMsg       struct{ err error }
	pulseMsg      struct{}
	approvalMsg   struct {
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

type chatModel struct {
	startTurn func(string) context.CancelFunc
	reset     func() // starts a new session: revokes session grants, clears history

	vp   viewport.Model
	ta   textarea.Model
	spin spinner.Model
	md   *glamour.TermRenderer

	history    string
	stream     string
	activeTool string // in-flight tool call (pulsing); "" when none
	pulse      int
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

// commitActiveTool moves the in-flight tool line into the static transcript.
func (m *chatModel) commitActiveTool() {
	if m.activeTool != "" {
		m.history += toolStyle.Render("  ● "+m.activeTool) + "\n"
		m.activeTool = ""
	}
}

func (m *chatModel) syncViewport() {
	content := m.history
	if m.activeTool != "" {
		dot := lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color(pulsePalette[m.pulse%len(pulsePalette)])).Render("●")
		content += "  " + dot + " " + toolStyle.Render(m.activeTool) + "\n"
	}
	if m.stream != "" {
		content += m.stream
	}
	m.vp.SetContent(content)
	m.vp.GotoBottom()
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
			m.stream = ""
			m.activeTool = ""
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
			m.history += userStyle.Render("❯ "+input) + "\n\n"
			m.stream = ""
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
		if m.activeTool != "" {
			m.syncViewport()
		}
		return m, pulseTick()

	case tokenMsg:
		m.commitActiveTool()
		m.stream += string(msg)
		m.syncViewport()
		return m, nil

	case toolMsg:
		m.commitActiveTool()
		m.activeTool = msg.name + "(" + msg.args + ")"
		m.syncViewport()
		return m, nil

	case toolResultMsg:
		if m.activeTool != "" {
			if msg.err != nil {
				m.history += errStyle.Render("  ● "+m.activeTool) + "\n"
				m.history += errStyle.Render("    ↳ "+shortErr(msg.err)) + "\n"
			} else {
				m.history += toolStyle.Render("  ● "+m.activeTool) + "\n"
			}
			m.activeTool = ""
		}
		m.syncViewport()
		return m, nil

	case approvalMsg:
		m.approval = &approval{intent: msg.intent, options: msg.options, reply: msg.reply}
		m.layout()
		m.syncViewport()
		return m, nil

	case doneMsg:
		m.commitActiveTool()
		m.cancel = nil
		if strings.TrimSpace(m.stream) != "" {
			m.history += m.renderMarkdown(m.stream) + "\n\n"
		}
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.history += hintStyle.Render("— cancelled") + "\n\n"
			} else {
				m.history += errStyle.Render("error: "+msg.err.Error()) + "\n\n"
			}
		}
		m.stream = ""
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
		HTTP: &http.Client{Timeout: 15 * time.Second},
	}
	tools := map[string]brain.Tool{}
	for _, t := range netCap.Tools() {
		tools[t.Name] = t
	}

	var p *tea.Program
	b := &brain.Brain{
		Model:       llm.New(baseURL, apiKey, modelName),
		Tools:       tools,
		ToolTimeout:  20 * time.Second,
		OnToken:      func(tok string) { p.Send(tokenMsg(tok)) },
		OnToolCall:   func(tc brain.ToolCall) { p.Send(toolMsg{name: tc.Tool, args: tc.Args}) },
		OnToolResult: func(_ brain.ToolCall, _ string, err error) { p.Send(toolResultMsg{err: err}) },
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
