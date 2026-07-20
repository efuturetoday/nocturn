// Command chat is a minimal terminal chat over agentkit + the OpenAI adapter: it streams the
// model's answer token-by-token and keeps conversation history across turns.
//
// Run it from the repo root (it reads FREELLM_BASE_URL / FREELLM_API_KEY / FREELLM_MODEL, and loads
// a .env if present):
//
//	go run ./agentkit-example
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

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

	sess := agentkit.NewSession(ctx, llm,
		agentkit.WithSystem("You are a concise, helpful assistant."),
	)
	defer sess.Close()

	// Render the event stream. turnDone signals the input loop when a turn finishes.
	turnDone := make(chan struct{}, 1)
	go func() {
		for ev := range sess.Subscribe() {
			switch e := ev.(type) {
			case agentkit.Token:
				fmt.Print(e.Text)
			case agentkit.ToolStart:
				fmt.Printf("\n  → %s(%s)\n", e.Tool, e.Args)
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

	fmt.Printf("agentkit chat (model %q) — /quit or Ctrl+C to exit\n", model)
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		if !in.Scan() {
			return // EOF
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
}
