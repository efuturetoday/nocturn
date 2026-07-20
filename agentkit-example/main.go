// Command chat wires the whole stack into a terminal chat: the OpenAI adapter (agentkit-openai), a
// toolset with a sub-agent, permission gating with human approval (agentkit-gate), assembled by
// agentkit-runtime. It streams the answer, keeps history, and prompts for approval before a guarded
// tool (notify_user) runs.
//
// Run it from the repo root (reads FREELLM_BASE_URL / FREELLM_API_KEY / FREELLM_MODEL, loads .env):
//
//	go run ./agentkit-example
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/efuturetoday/agentkit"
	"github.com/efuturetoday/agentkit-gate"
	"github.com/efuturetoday/agentkit-openai"
	"github.com/efuturetoday/agentkit-runtime"
)

func main() {
	loadDotEnv(".env")

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

	// One stdin reader, shared by the chat loop and the approval prompt (never concurrent: a turn
	// blocks the input loop, so stdin is free while the approver asks).
	stdin := bufio.NewReader(os.Stdin)

	llm := openai.New(baseURL, apiKey, model,
		openai.WithEffort(agentkit.Effort(os.Getenv("FREELLM_REASONING_EFFORT"))),
	)

	tools, err := buildTools(llm)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tools:", err)
		os.Exit(1)
	}
	skills, err := buildSkills()
	if err != nil {
		fmt.Fprintln(os.Stderr, "skills:", err)
		os.Exit(1)
	}

	// notify_user is guarded → asks the human; everything else runs freely.
	policy := gate.Classify([]string{"notify_user"}, nil)

	rt := runtime.New(llm,
		runtime.WithTools(tools),
		runtime.WithSkills(skills),
		runtime.WithGate(policy, nil, &consoleApprover{in: stdin}),
		runtime.WithSession(
			agentkit.WithSystem("You are a concise, helpful assistant. Use tools when useful, call "+
				"load_skill for skills, delegate poetry to the poet sub-agent, and notify_user to ping the user."),
			agentkit.WithTokenizer(agentkit.ApproxTokenizer()),
			agentkit.WithTimeout(2*time.Minute),
		),
	)

	sess := rt.Session(ctx)
	defer sess.Close()

	turnDone := make(chan struct{}, 1)
	go func() {
		for ev := range sess.Subscribe() {
			switch e := ev.(type) {
			case agentkit.Token:
				if e.Frame == 0 {
					fmt.Print(e.Text)
				} else {
					fmt.Printf("\033[36m%s\033[0m", e.Text) // sub-agent output, cyan
				}
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
				case turnDone <- struct{}{}:
				default:
				}
			}
		}
	}()

	fmt.Printf("agentkit chat (model %q) — tools: current_time, add, poet, notify_user · skill: haiku · /quit\n", model)
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
		if line == "/quit" || line == "/exit" {
			return
		}
		sess.Submit(line)
		select {
		case <-turnDone:
		case <-ctx.Done():
			return
		}
	}
}

// consoleApprover asks for approval on the terminal, sharing the chat's stdin reader.
type consoleApprover struct {
	in *bufio.Reader
}

func (c *consoleApprover) Ask(_ context.Context, a gate.Action) (gate.Decision, gate.Scope, error) {
	target := ""
	if a.Target != "" {
		target = " → " + a.Target
	}
	fmt.Printf("\n  [approve] %s%s ? [y=once / a=always / N] ", a.Tool, target)
	line, _ := c.in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y":
		return gate.Allow, gate.Once, nil
	case "a":
		return gate.Allow, gate.Always, nil
	default:
		return gate.Deny, gate.Once, nil
	}
}

// buildTools defines plain tools, a guarded tool, and a sub-agent exposed as a tool.
func buildTools(llm agentkit.LLM) (agentkit.ToolSet, error) {
	timeTool, err := agentkit.NewTool("current_time", "Return the current local time.",
		func(context.Context, string) (string, error) {
			return time.Now().Format(time.RFC1123), nil
		})
	if err != nil {
		return nil, err
	}

	addTool, err := agentkit.NewTool("add", "Add two numbers a and b.",
		func(_ context.Context, args string) (string, error) {
			var in struct {
				A float64 `json:"a"`
				B float64 `json:"b"`
			}
			if err := json.Unmarshal([]byte(args), &in); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			return strconv.FormatFloat(in.A+in.B, 'f', -1, 64), nil
		},
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("a", agentkit.Number("first addend")),
			agentkit.Prop("b", agentkit.Number("second addend")),
		).Require("a", "b")),
	)
	if err != nil {
		return nil, err
	}

	notifyTool, err := agentkit.NewTool("notify_user", "Send a short notification to the user.",
		func(_ context.Context, args string) (string, error) {
			var in struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(args), &in); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			fmt.Printf("\n  📣 %s\n", in.Message)
			return "delivered", nil
		},
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("message", agentkit.String("the message to show the user")),
		).Require("message")),
	)
	if err != nil {
		return nil, err
	}

	poet := agentkit.Agent{
		Name:         "poet",
		Instructions: "You are a poet. Reply with a single short, vivid poem about the given topic. No preamble.",
	}
	poetTool := agentkit.AgentTool(poet, llm, nil)

	return agentkit.NewToolSet(timeTool, addTool, notifyTool, poetTool)
}

// buildSkills defines one progressive-disclosure skill the model can load on demand.
func buildSkills() (agentkit.SkillSet, error) {
	return agentkit.NewSkillSet(agentkit.Skill{
		Name:        "haiku",
		Description: "How to write a haiku. Load this when the user asks for a haiku or a Japanese poem.",
		Body:        "A haiku is three lines of 5, 7, and 5 syllables. Keep it about nature or a season. No title.",
	})
}

// loadDotEnv loads KEY=VALUE lines from path into the environment (existing vars win). A missing file
// is ignored. Minimal, stdlib-only — the example pulls no config dependency.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
	_ = sc.Err() // best-effort .env load
}
