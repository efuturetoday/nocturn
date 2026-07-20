// Command chat is a minimal terminal chat over agentkit + the OpenAI adapter. It wires up the full
// surface — tools, skills and a sub-agent — streams the answer token-by-token, and keeps history.
//
// Run it from the repo root (it reads FREELLM_BASE_URL / FREELLM_API_KEY / FREELLM_MODEL, and loads
// a .env if present):
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
	openai "github.com/efuturetoday/agentkit-openai"
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

	sess := agentkit.NewSession(ctx, llm,
		agentkit.WithSystem("You are a concise, helpful assistant. Use tools when useful, "+
			"call load_skill to load a skill's instructions, and delegate poetry to the poet sub-agent."),
		agentkit.WithTools(tools),
		agentkit.WithSkills(skills),
		// The freellm proxy omits usage; fall back to a rough estimate so [tokens] is meaningful.
		agentkit.WithTokenizer(agentkit.ApproxTokenizer()),
	)
	defer sess.Close()

	// Render the event stream. turnDone signals the input loop when a turn finishes.
	turnDone := make(chan struct{}, 1)
	go func() {
		for ev := range sess.Subscribe() {
			switch e := ev.(type) {
			case agentkit.Token:
				if e.Frame == 0 {
					fmt.Print(e.Text) // main agent
				} else {
					fmt.Printf("\033[36m%s\033[0m", e.Text) // sub-agent output, cyan
				}
			case agentkit.Thinking:
				fmt.Printf("\033[2m%s\033[0m", e.Text) // reasoning, dimmed
			case agentkit.ToolStart:
				fmt.Printf("\n  → %s(%s)\n", e.Tool, e.Args)
			case agentkit.ToolEnd:
				if e.Err != nil {
					fmt.Printf("  ← %s: error: %v\n", e.Tool, e.Err)
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

	fmt.Printf("agentkit chat (model %q) — tools: current_time, add, poet · skill: haiku · /quit to exit\n", model)
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		if !in.Scan() {
			if err := in.Err(); err != nil {
				fmt.Fprintln(os.Stderr, "read:", err)
			}
			return
		}
		line := strings.TrimSpace(in.Text())
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

// buildTools defines two plain tools and one sub-agent exposed as a tool.
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
		agentkit.WithSchema(json.RawMessage(
			`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}`)),
	)
	if err != nil {
		return nil, err
	}

	// A leaf sub-agent (no tools of its own), exposed to the main agent as a tool named "poet".
	poet := agentkit.Agent{
		Name:         "poet",
		Instructions: "You are a poet. Reply with a single short, vivid poem about the given topic. No preamble.",
	}
	poetTool := agentkit.AgentTool(poet, llm, nil) // nil = leaf, no tools

	return agentkit.NewToolSet(timeTool, addTool, poetTool)
}

// buildSkills defines one progressive-disclosure skill the model can load on demand.
func buildSkills() (agentkit.SkillSet, error) {
	return agentkit.NewSkillSet(agentkit.Skill{
		Name:        "haiku",
		Description: "How to write a haiku. Load this when the user asks for a haiku or a Japanese poem.",
		Body:        "A haiku is three lines of 5, 7, and 5 syllables. Keep it about nature or a season. No title.",
	})
}

// loadDotEnv loads KEY=VALUE lines from path into the environment (existing vars win). A missing
// file is ignored. Minimal, stdlib-only — the example pulls no config dependency.
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
	_ = sc.Err() // best-effort .env load; a read error just means fewer vars
}
