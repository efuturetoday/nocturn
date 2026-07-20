// Package workspace is the composition root per workspace: it bundles the tools (the cage), the
// permission gate (policy + durable grants + approver), the persona, and the chat store and manager
// into one aggregate over a shared Host. app/main opens one; multiplexing several is a later slice.
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/app/chat"
	"github.com/efuturetoday/nocturn/app/net"
)

const turnTimeout = 2 * time.Minute

// Host is the process-wide wiring shared by every workspace: the LLM endpoint and the one human
// approver (one device). It grows as more shared services arrive (notify, log, master key).
type Host struct {
	LLM      agentkit.LLM
	Approver gate.Approver
}

// Workspace is one isolated stack: its own tools, grants, persona, and chats over the Host.
type Workspace struct {
	name       string
	dir        string
	llm        agentkit.LLM
	tools      agentkit.ToolSet
	grants     gate.Grants
	chats      *chat.Manager // user chats
	agentStore *chat.Store   // agent run transcripts (SourceAgent)
	agents     []Agent
	sched      *scheduler
}

// Open builds (creating its directory if needed) a workspace named name rooted at dir: it assembles
// the toolset, the durable grant store, the persona, a gated runtime, and a chat store + manager.
func Open(h Host, name, dir string) (*Workspace, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace %q: %w", name, err)
	}

	httpTool, err := net.New().Tool()
	if err != nil {
		return nil, fmt.Errorf("workspace %q: net tool: %w", name, err)
	}
	tools, err := agentkit.NewToolSet(httpTool)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: toolset: %w", name, err)
	}

	gs, err := newGrantStore(filepath.Join(dir, "grants.json"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: grants: %w", name, err)
	}

	rt := runtime.New(h.LLM,
		runtime.WithTools(tools),
		runtime.WithGate(policy(), gs, h.Approver),
		runtime.WithSession(
			agentkit.WithSystem(resolvePersona(dir)),
			agentkit.WithTimeout(turnTimeout),
		),
	)

	userStore, err := chat.NewStore(filepath.Join(dir, "chats"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: chat store: %w", name, err)
	}
	agentStore, err := chat.NewStore(filepath.Join(dir, "agent-runs"), chat.WithSource(chat.SourceAgent))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: agent store: %w", name, err)
	}

	agents, err := discoverAgents(dir)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: agents: %w", name, err)
	}

	w := &Workspace{
		name:       name,
		dir:        dir,
		llm:        h.LLM,
		tools:      tools,
		grants:     gs,
		chats:      chat.NewManager(rt, userStore),
		agentStore: agentStore,
		agents:     agents,
	}
	w.sched = newScheduler(agents, func(ctx context.Context, a Agent) {
		_, _ = w.FireAgent(ctx, a.Name, "Run your scheduled task now.")
	})
	return w, nil
}

// Name returns the workspace name.
func (w *Workspace) Name() string { return w.name }

// Chats returns the workspace's chat manager.
func (w *Workspace) Chats() *chat.Manager { return w.chats }

// Agents returns the workspace's declared agents.
func (w *Workspace) Agents() []Agent { return w.agents }

// StartAgents runs the cron scheduler until ctx is cancelled — call it in a goroutine.
func (w *Workspace) StartAgents(ctx context.Context) { w.sched.start(ctx) }

// FireAgent runs the named agent once, unattended, over task; it persists the transcript to the
// agent store and returns the agent's final answer. Unattended = no approver: the agent may use
// standing durable grants, but any fresh Ask is denied (fail-closed). Its tool cage is the workspace
// toolset filtered to the agent's declared tools.
func (w *Workspace) FireAgent(ctx context.Context, name, task string) (string, error) {
	a, ok := w.agent(name)
	if !ok {
		return "", fmt.Errorf("workspace %q: no agent %q", w.name, name)
	}

	rt := runtime.New(w.llm,
		runtime.WithTools(w.tools.Select(a.Matches)),
		runtime.WithGate(policy(), w.grants, nil), // nil approver = unattended
		runtime.WithSession(
			agentkit.WithSystem(a.Instructions),
			agentkit.WithEffort(a.Effort),
			agentkit.WithTimeout(turnTimeout),
		),
	)

	sess := rt.Session(ctx, agentkit.WithStore(w.agentStore, chat.NewID()))
	defer sess.Close()

	var answer strings.Builder
	done := make(chan error, 1)
	go func() {
		for ev := range sess.Subscribe() {
			switch e := ev.(type) {
			case agentkit.Token:
				if e.Frame == 0 {
					answer.WriteString(e.Text)
				}
			case agentkit.TurnEnd:
				done <- e.Err
				return
			}
		}
		done <- nil
	}()
	sess.Submit(task)
	select {
	case err := <-done:
		return answer.String(), err
	case <-ctx.Done():
		return answer.String(), ctx.Err()
	}
}

// AgentRuns lists the persisted agent-run transcripts, most recent first.
func (w *Workspace) AgentRuns() ([]chat.Meta, error) { return w.agentStore.Metas() }

func (w *Workspace) agent(name string) (Agent, bool) {
	for _, a := range w.agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// policy is the workspace-root policy: the net axis asks the human (remembered for the session);
// every other Kind runs free. Per-agent policies (stricter) come with the agent slice.
func policy() gate.Policy {
	return gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		switch a.Kind {
		case net.Axis:
			return gate.AskWith(gate.RecallSession)
		default:
			return gate.Allowed()
		}
	})
}

// defaultPersona is the built-in system prompt used when a workspace has no PERSONA.md.
const defaultPersona = "You are nocturn, a concise, helpful assistant. Use http_get when a URL is useful."

// resolvePersona returns the workspace system prompt: the PERSONA.md override in the workspace root
// if present and non-empty, else defaultPersona. PERSONA.md lives in the ROOT — control-plane, never
// a tool-reachable path — so the model can neither read nor rewrite its own identity; a self-writable
// persona would be a prompt-injection vector onto the assistant itself.
func resolvePersona(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "PERSONA.md"))
	if err != nil {
		return defaultPersona
	}
	if body := strings.TrimSpace(string(data)); body != "" {
		return body
	}
	return defaultPersona
}
