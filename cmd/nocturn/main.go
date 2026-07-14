// Command nocturn runs skills and chats through Nocturn's security path: every
// effect is brokered, and sensitive ones require human approval (on this
// terminal, or out of band via ntfy).
//
// Usage:
//
//	nocturn run  <skill.wasm>                 # run a WASM skill (log window)
//	nocturn chat [--allow-fetch] "<request>"  # ask the LLM; it may fetch, gated
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/hitl/ntfy"
	"github.com/efuturetoday/nocturn/internal/host"
	"github.com/efuturetoday/nocturn/internal/llm"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: nocturn <run|chat|tui> ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "chat":
		err = chatCmd(os.Args[2:])
	case "tui":
		err = tuiCmd(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: nocturn <run|chat|tui> ...")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// ---- chat: LLM + brain + gateway-guarded tools ----

func chatCmd(args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	allowFetch := fs.Bool("allow-fetch", false, "auto-allow net.read reads (writes always require approval)")
	useNtfy := fs.Bool("ntfy", false, "approve out of band via ntfy instead of this terminal")
	ntfyURL := fs.String("ntfy-url", "https://ntfy.sh", "ntfy server base URL")
	reqTopic := fs.String("req-topic", "", "ntfy topic for approval requests")
	respTopic := fs.String("resp-topic", "", "ntfy topic for decisions")
	ttl := fs.Duration("ttl", 2*time.Minute, "approval wait time")
	_ = fs.Parse(args)

	request := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if request == "" {
		return errors.New(`usage: nocturn chat "<request>"`)
	}

	_ = godotenv.Load() // load .env if present; real env vars still win
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

	engine, err := buildEngine(ctx, *useNtfy, *ntfyURL, *reqTopic, *respTopic)
	if err != nil {
		return err
	}

	effect := capability.Ask
	if *allowFetch {
		effect = capability.Allow
	}
	epochs := capability.NewEpochRegistry()
	netCap := &gateway.Net{
		Guard: &gateway.Guard{
			Policy: capability.Policy{Rules: []capability.Rule{
				{Capability: "http.read", HostGlob: capability.Wildcard, Effect: effect, Epoch: capability.Permanent},
				// writes always require approval — never auto-allowed by --allow-fetch.
				{Capability: "http.write", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
				{Capability: "dns.resolve", HostGlob: capability.Wildcard, Effect: effect, Epoch: capability.Permanent},
			}},
			Approvals: engine,
			Epochs:    epochs, // shared with the session, so "Allow this session" grants are epoch-scoped
			TTL:       *ttl,
		},
		HTTP: &http.Client{Timeout: 15 * time.Second},
	}

	// The capabilities export their own tools (name, schema, argument validation).
	tools := map[string]brain.Tool{}
	for _, t := range netCap.Tools() {
		tools[t.Name] = t
	}

	b := &brain.Brain{
		Model:       llm.New(baseURL, apiKey, modelName),
		Tools:       tools,
		ToolTimeout: 20 * time.Second,
		OnToken:     func(tok string) { fmt.Print(tok) },                                    // stream the answer live
		OnToolCall:  func(tc brain.ToolCall) { fmt.Printf("  → %s(%s)\n", tc.Tool, tc.Args) }, // UI feedback
	}
	session := agent.New(b, netCap.Guard, epochs)
	defer session.Close()
	if _, err := session.Ask(ctx, request); err != nil {
		return err
	}
	fmt.Println()
	return nil
}

// ---- run: a WASM skill through the HITL log window ----

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	useNtfy := fs.Bool("ntfy", false, "approve out of band via ntfy (phone)")
	ntfyURL := fs.String("ntfy-url", "https://ntfy.sh", "ntfy server base URL")
	reqTopic := fs.String("req-topic", "", "ntfy topic for approval requests")
	respTopic := fs.String("resp-topic", "", "ntfy topic for decisions")
	ttl := fs.Duration("ttl", 2*time.Minute, "approval wait time")
	_ = fs.Parse(args)

	wasmPath := fs.Arg(0)
	if wasmPath == "" {
		return errors.New("no skill given: nocturn run <skill.wasm>")
	}
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return fmt.Errorf("reading skill: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	engine, err := buildEngine(ctx, *useNtfy, *ntfyURL, *reqTopic, *respTopic)
	if err != nil {
		return err
	}

	policy := capability.Policy{Rules: []capability.Rule{
		{Capability: "log", Effect: capability.Ask, Epoch: capability.Permanent},
	}}
	return host.RunWithHITLLog(ctx, wasm, policy, engine, *ttl, func(text string) {
		fmt.Printf("skill output: %s\n", text)
	})
}

// ---- shared: the approval engine (console default, ntfy opt-in) ----

func buildEngine(ctx context.Context, useNtfy bool, ntfyURL, reqTopic, respTopic string) (*hitl.Engine, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}

	if useNtfy {
		if reqTopic == "" || respTopic == "" {
			return nil, errors.New("--ntfy requires --req-topic and --resp-topic")
		}
		pub := ntfy.New(ntfyURL, reqTopic, ntfyURL+"/"+respTopic)
		engine := hitl.NewEngine(key, pub)
		lis := ntfy.NewListener(ntfyURL, respTopic, engine.Resolve)
		go func() { _ = lis.Run(ctx) }()
		fmt.Printf("Approvals go to ntfy topic %q — subscribe on your phone to approve.\n", reqTopic)
		return engine, nil
	}

	cn := &consoleNotifier{}
	engine := hitl.NewEngine(key, cn)
	cn.resolve = engine.Resolve
	return engine, nil
}

// consoleNotifier approves on the terminal.
type consoleNotifier struct {
	resolve func(token string) error
}

func (c *consoleNotifier) Notify(intent string, options []hitl.Option) error {
	fmt.Printf("\nApproval needed: %s\n", intent)
	for i, o := range options {
		fmt.Printf("  [%d] %s\n", i+1, o.Label)
	}
	fmt.Print("  choose: ")

	var answer string
	_, _ = fmt.Fscanln(os.Stdin, &answer)
	if idx, err := strconv.Atoi(strings.TrimSpace(answer)); err == nil && idx >= 1 && idx <= len(options) {
		return c.resolve(options[idx-1].Token)
	}
	// unrecognised input -> deny (fail closed)
	for _, o := range options {
		if o.Outcome == hitl.Denied {
			return c.resolve(o.Token)
		}
	}
	return nil
}
