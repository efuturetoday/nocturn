// Package workspace is the composition root per workspace: it bundles the tools (the cage), the
// permission gate (policy + durable grants + approver), the persona, and the chat store and manager
// into one aggregate over a shared Host. app/main opens one; multiplexing several is a later slice.
package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/app/agent"
	"github.com/efuturetoday/nocturn/app/chat"
	"github.com/efuturetoday/nocturn/app/tools"
	"github.com/efuturetoday/nocturn/internal/secret"
)

const turnTimeout = 2 * time.Minute

// DefaultWorkspace is the workspace a fresh install and the terminal always have.
const DefaultWorkspace = "main"

// Host is the process-wide wiring shared by every workspace: the LLM endpoint and the one human
// approver (one device). It grows as more shared services arrive (notify, log, master key).
type Host struct {
	LLM      agentkit.LLM
	Approver gate.Approver
	Secrets  *secret.Injector // host-owned credential jar the network tool injects from; nil = none
	Log      *slog.Logger
}

// Workspace is one isolated stack: its own tools, grants, persona, and chats over the Host.
type Workspace struct {
	name       string
	dir        string
	llm        agentkit.LLM
	tools      agentkit.ToolSet
	grants     gate.Grants
	chats      *chat.Manager // user chats
	userStore  *chat.Store   // the user chat store (behind chats)
	agentStore *chat.Store   // agent run transcripts (SourceAgent)
	agents     agent.Set
	sched      *agent.Scheduler
	log        *slog.Logger
}

// Open builds (creating its directory if needed) a workspace named name rooted at dir: it assembles
// the toolset, the durable grant store, the persona, a gated runtime, and a chat store + manager.
func Open(h Host, name, dir string) (*Workspace, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace %q: %w", name, err)
	}

	agents, err := agent.Discover(filepath.Join(dir, "agents"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: agents: %w", name, err)
	}

	toolset, err := buildTools(h.LLM, agents, h.Secrets)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: toolset: %w", name, err)
	}

	gs, err := newGrantStore(filepath.Join(dir, "grants.json"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: grants: %w", name, err)
	}

	rt := runtime.New(h.LLM,
		runtime.WithTools(toolset),
		runtime.WithGate(policy(), gs, h.Approver),
		runtime.WithSession(
			agentkit.WithSystem(resolvePersona(dir)),
			agentkit.WithTimeout(turnTimeout),
			agentkit.WithLogger(agentkit.SlogLogger(h.Log)),
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

	w := &Workspace{
		name:       name,
		dir:        dir,
		llm:        h.LLM,
		tools:      toolset,
		grants:     gs,
		chats:      chat.NewManager(rt, userStore),
		userStore:  userStore,
		agentStore: agentStore,
		agents:     agents,
		log:        h.Log,
	}
	w.sched = agent.NewScheduler(agents, func(ctx context.Context, a agent.Agent) {
		_, _ = w.FireAgent(ctx, a.Name, "Run your scheduled task now.")
	})
	return w, nil
}

// OpenAll opens every workspace under root (each subdirectory is one, by name), always including a
// "main" so a fresh install and the terminal have a default. It is the daemon's workspace registry.
func OpenAll(h Host, root string) (map[string]*Workspace, error) {
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	spaces := map[string]*Workspace{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ws, err := Open(h, e.Name(), filepath.Join(root, e.Name()))
		if err != nil {
			return nil, err
		}
		spaces[e.Name()] = ws
	}
	if _, ok := spaces[DefaultWorkspace]; !ok {
		ws, err := Open(h, DefaultWorkspace, filepath.Join(root, DefaultWorkspace))
		if err != nil {
			return nil, err
		}
		spaces[DefaultWorkspace] = ws
	}
	return spaces, nil
}

// Name returns the workspace name.
func (w *Workspace) Name() string { return w.name }

// Chats returns the workspace's chat manager.
func (w *Workspace) Chats() *chat.Manager { return w.chats }

// OnChatUpdate wires a callback fired whenever any chat here changes (a turn save, a markRead), for
// pushing chat activity — both user chats and agent runs.
func (w *Workspace) OnChatUpdate(fn func(chat.Meta)) {
	w.userStore.OnSave(fn)
	w.agentStore.OnSave(fn)
}

// MarkRead advances a chat's shared read cursor (user chat or agent run; the wrong store no-ops).
func (w *Workspace) MarkRead(id string) {
	_ = w.userStore.MarkRead(id)
	_ = w.agentStore.MarkRead(id)
}

// buildTools assembles the workspace toolset: the base tools plus code_run (the root chat's cage),
// and each declared agent exposed as a sub-agent tool scoped to its OWN cage — its filtered subset of
// the base tools, plus code_run only if the agent declares it, dispatching over that same subset.
// code_run is woven per cage (tools.Compose), so a script never reaches past the cage it runs in.
func buildTools(llm agentkit.LLM, agents agent.Set, creds *secret.Injector) (agentkit.ToolSet, error) {
	baseTools, err := tools.Base(creds)
	if err != nil {
		return agentkit.ToolSet{}, err
	}
	base, err := agentkit.NewToolSet(baseTools...)
	if err != nil {
		return agentkit.ToolSet{}, err
	}

	// Root chat cage: every base tool + code_run dispatching over them.
	rootSet, err := tools.Compose(base, true)
	if err != nil {
		return agentkit.ToolSet{}, err
	}
	all := make([]agentkit.Tool, 0, len(rootSet)+len(agents.All()))
	for _, t := range rootSet {
		all = append(all, t)
	}

	for _, a := range agents.All() {
		// The agent's cage is its filtered base subset; code_run is added only if it declares it,
		// and then dispatches over exactly that subset — no widening.
		cage, err := tools.Compose(base.Select(a.Matches), a.Matches(tools.CodeRunTool))
		if err != nil {
			return agentkit.ToolSet{}, err
		}
		sub := agentkit.AgentTool(
			agentkit.Agent{Name: a.Name, Instructions: a.Instructions, Effort: a.Effort},
			llm, cage,
		)
		all = append(all, sub)
	}
	return agentkit.NewToolSet(all...)
}

// policy is the workspace-root policy: the net axis asks the human (remembered for the session);
// every other Kind runs free. Per-agent policies (stricter) come with the agent slice.
func policy() gate.Policy {
	return gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		switch a.Kind {
		case tools.NetAxis:
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
