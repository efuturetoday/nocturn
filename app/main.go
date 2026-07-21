// Command app is nocturn rebuilt on agentkit: a terminal chat driven by agentkit Sessions, their
// tools gated by agentkit/gate with human approval on the terminal, and conversations persisted and
// multiplexed by app/chat — all composed per workspace by app/workspace. This is the greenfield
// root; the old cmd/nocturn still stands.
//
// Reads FREELLM_BASE_URL / FREELLM_API_KEY / FREELLM_MODEL (loads .env). Run: go run ./app
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/openai"
	"github.com/efuturetoday/nocturn/app/auth"
	"github.com/efuturetoday/nocturn/app/chat"
	"github.com/efuturetoday/nocturn/app/hitl"
	"github.com/efuturetoday/nocturn/app/serve"
	"github.com/efuturetoday/nocturn/app/workspace"
)

const wsDir = "./nocturn-data/workspaces/main"

func main() {
	serveAddr := flag.String("serve", "", "run a WebSocket daemon at this address instead of the terminal (e.g. :8080)")
	flag.Parse()

	_ = godotenv.Load()

	baseURL := os.Getenv("FREELLM_BASE_URL")
	apiKey := os.Getenv("FREELLM_API_KEY")
	model := os.Getenv("FREELLM_MODEL")
	if model == "" {
		model = "auto"
	}
	if baseURL == "" && apiKey == "" {
		fmt.Fprintln(os.Stderr, "set FREELLM_BASE_URL / FREELLM_API_KEY / FREELLM_MODEL (or a .env)")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// One stdin reader, shared by the chat loop and the approval prompt: a turn blocks the input
	// loop, so stdin is free while the approver asks.
	stdin := bufio.NewReader(os.Stdin)

	// Logs go to stderr (structured); the terminal UI keeps stdout. In daemon mode, stderr is the
	// operator's window into the running backend.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	llm := openai.New(baseURL, apiKey, model,
		openai.WithEffort(agentkit.Effort(os.Getenv("FREELLM_REASONING_EFFORT"))),
		openai.WithLogger(agentkit.SlogLogger(logger)),
	)

	// The terminal prompts inline; the daemon routes approvals out of band to a connected device via
	// the hitl broker (and, when none is attached, a placeholder push).
	var approver gate.Approver
	var broker *hitl.Broker
	if *serveAddr == "" {
		approver = &terminalApprover{in: stdin}
	} else {
		broker = hitl.NewBroker(hitl.NewLogPusher(logger), logger)
		approver = broker
	}
	host := workspace.Host{LLM: llm, Approver: approver, Log: logger}

	ws, err := workspace.Open(host, "main", wsDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go ws.StartAgents(ctx) // cron scheduler for declared agents

	if *serveAddr != "" {
		devices, err := auth.New("./nocturn-data/devices.json")
		if err != nil {
			fmt.Fprintln(os.Stderr, "auth:", err)
			os.Exit(1)
		}
		fmt.Printf("nocturn daemon — ws on %s (model %q)\n", *serveAddr, model)
		spaces := map[string]*workspace.Workspace{"main": ws} // a workspace registry is a later slice
		if err := serve.Serve(ctx, *serveAddr, spaces, devices, broker, logger); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
		return
	}

	run(ctx, ws, stdin, model)
}

// run is the terminal loop: the first message (or one after /new) starts a chat; /chats lists,
// /open resumes, /quit exits. One chat is active at a time — switching closes the previous session
// (its transcript is already persisted and reloads on resume).
func run(ctx context.Context, ws *workspace.Workspace, stdin *bufio.Reader, model string) {
	mgr := ws.Chats()
	turnDone := make(chan struct{}, 1)
	var active *agentkit.Session

	activate := func(sess *agentkit.Session) {
		if active != nil {
			active.Close()
		}
		active = sess
		go render(sess, turnDone)
	}
	defer func() {
		if active != nil {
			active.Close()
		}
	}()

	fmt.Printf("nocturn (model %q) — /chats · /open <id> · /new · /agents · /fire <name> <task> · /quit\n", model)
	fmt.Print("\ntype a message to start a chat.\n")
	for {
		fmt.Print("\n> ")
		line, err := stdin.ReadString('\n')
		if err != nil {
			return // EOF
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		switch {
		case line == "/quit" || line == "/exit":
			return
		case line == "/chats":
			listChats(mgr)
			continue
		case line == "/agents":
			listAgents(ws)
			continue
		case strings.HasPrefix(line, "/fire "):
			fireAgent(ctx, ws, strings.TrimPrefix(line, "/fire "))
			continue
		case line == "/new":
			if active != nil {
				active.Close()
				active = nil
			}
			fmt.Println("new chat — type your first message.")
			continue
		case strings.HasPrefix(line, "/open "):
			id := strings.TrimSpace(strings.TrimPrefix(line, "/open "))
			activate(mgr.Open(ctx, id))
			fmt.Printf("opened %s — type to continue.\n", id)
			continue
		}

		// A plain line: start a new chat, or continue the active one.
		if active == nil {
			_, sess := mgr.Start(ctx, line)
			activate(sess)
		} else {
			active.Submit(line)
		}
		select {
		case <-turnDone:
		case <-ctx.Done():
			return
		}
	}
}

func listChats(mgr *chat.Manager) {
	metas, err := mgr.List()
	if err != nil {
		fmt.Println("list:", err)
		return
	}
	if len(metas) == 0 {
		fmt.Println("(no chats yet)")
		return
	}
	for _, m := range metas {
		fmt.Printf("  %s  %-42s  %d turns  %s\n", m.ID, m.Name, m.Turns, m.Updated.Format("Jan 2 15:04"))
	}
}

func listAgents(ws *workspace.Workspace) {
	agents := ws.Agents()
	if len(agents) == 0 {
		fmt.Println("(no agents — add one at agents/<name>/agent.md)")
		return
	}
	for _, a := range agents {
		when := a.When
		if when == "" {
			when = "manual"
		}
		fmt.Printf("  %-16s tools:%v  when:%s\n", a.Name, a.Tools, when)
	}
}

func fireAgent(ctx context.Context, ws *workspace.Workspace, rest string) {
	name, task, _ := strings.Cut(strings.TrimSpace(rest), " ")
	if name == "" {
		fmt.Println("usage: /fire <name> <task>")
		return
	}
	fmt.Printf("firing %s (unattended)…\n", name)
	answer, err := ws.FireAgent(ctx, name, strings.TrimSpace(task))
	if err != nil {
		fmt.Println("agent:", err)
	}
	if answer != "" {
		fmt.Println(answer)
	}
}

// render drains one session's event stream to the terminal and signals turnDone on each TurnEnd. It
// ends when the session is closed (the stream closes).
func render(sess *agentkit.Session, done chan<- struct{}) {
	for ev := range sess.Subscribe() {
		switch e := ev.(type) {
		case agentkit.Token:
			fmt.Print(e.Text)
		case agentkit.Thinking:
			fmt.Printf("\033[2m%s\033[0m", e.Text)
		case agentkit.ToolStart:
			fmt.Printf("\n  → %s(%s)\n", e.Tool, e.Args)
		case agentkit.ToolEnd:
			if e.Err != nil {
				fmt.Printf("  ← %s: %v\n", e.Tool, e.Err)
			}
		case agentkit.TurnEnd:
			if e.Err != nil {
				fmt.Printf("\n[stopped: %v]", e.Err)
			}
			fmt.Printf("\n[tokens: %d]\n", e.Tokens.Total)
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}
}

// terminalApprover asks for approval on the terminal, sharing the chat's stdin reader.
type terminalApprover struct {
	in *bufio.Reader
}

func (t *terminalApprover) Ask(_ context.Context, a gate.Action, suggest []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	exact := gate.Grant{Kind: a.Kind, Target: a.Target}
	fmt.Print("\n  [approve] " + a.Kind)
	if a.Target != "" {
		fmt.Print(" → " + a.Target)
	}
	fmt.Print(" ? [y=session / a=always")
	for i, s := range suggest {
		fmt.Printf(" / %d=always %s", i+1, s.Target)
	}
	fmt.Print(" / N] ")

	line, _ := t.in.ReadString('\n')
	switch choice := strings.ToLower(strings.TrimSpace(line)); choice {
	case "y":
		return true, exact, gate.RecallSession, nil
	case "a":
		return true, exact, gate.RecallAlways, nil
	default:
		if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(suggest) {
			return true, suggest[n-1], gate.RecallAlways, nil
		}
		return false, gate.Grant{}, gate.RecallNever, nil
	}
}
