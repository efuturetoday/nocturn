// Command app is nocturn rebuilt on agentkit: a terminal chat driven by agentkit Sessions, their
// tools gated by agentkit/gate with human approval on the terminal, and conversations persisted and
// multiplexed by app/chat. This is the greenfield root — the new world grows here while the old
// cmd/nocturn still stands.
//
// Reads FREELLM_BASE_URL / FREELLM_API_KEY / FREELLM_MODEL (loads .env). Run: go run ./app
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/openai"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/app/chat"
	"github.com/efuturetoday/nocturn/app/net"
)

const chatDir = "./nocturn-data/chats"

func main() {
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

	rt, err := buildRuntime(baseURL, apiKey, model, stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	store, err := chat.NewStore(chatDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		os.Exit(1)
	}
	mgr := chat.NewManager(rt, store)

	run(ctx, mgr, stdin, model)
}

// buildRuntime wires the LLM, the gated toolset and session defaults into a Runtime shared by every
// chat.
func buildRuntime(baseURL, apiKey, model string, stdin *bufio.Reader) (*runtime.Runtime, error) {
	llm := openai.New(baseURL, apiKey, model,
		openai.WithEffort(agentkit.Effort(os.Getenv("FREELLM_REASONING_EFFORT"))),
	)

	httpTool, err := net.New().Tool()
	if err != nil {
		return nil, fmt.Errorf("net tool: %w", err)
	}
	tools, err := agentkit.NewToolSet(httpTool)
	if err != nil {
		return nil, fmt.Errorf("toolset: %w", err)
	}

	// The net axis asks the human (remembered for the session); every other Kind runs free.
	policy := gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		switch a.Kind {
		case net.Axis:
			return gate.AskWith(gate.RecallSession)
		default:
			return gate.Allowed()
		}
	})

	return runtime.New(llm,
		runtime.WithTools(tools),
		runtime.WithGate(policy, gate.NewMemGrants(), &terminalApprover{in: stdin}),
		runtime.WithSession(
			agentkit.WithSystem("You are nocturn, a concise, helpful assistant. Use http_get when a URL is useful."),
			agentkit.WithTimeout(2*time.Minute),
		),
	), nil
}

// run is the terminal loop: the first message (or one after /new) starts a chat; /chats lists,
// /open resumes, /quit exits. One chat is active at a time — switching closes the previous session
// (its transcript is already persisted and reloads on resume).
func run(ctx context.Context, mgr *chat.Manager, stdin *bufio.Reader, model string) {
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

	fmt.Printf("nocturn (model %q) — /chats · /open <id> · /new · /quit · tool: http_get\n", model)
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
