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
	"github.com/efuturetoday/nocturn/app/plugin"
	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/tools"
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
	Scanner  *secret.Scanner  // bidirectional secret leak scanner; nil = no scanning
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

	baseTools, err := tools.Base(h.Secrets, h.Scanner)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: tools: %w", name, err)
	}
	base, err := agentkit.NewToolSet(baseTools...)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: toolset: %w", name, err)
	}

	gs, err := newGrantStore(filepath.Join(dir, "grants.json"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: grants: %w", name, err)
	}

	toolset, err := buildTools(base, h.LLM, agents)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: toolset: %w", name, err)
	}

	// Discover + install plugins as top-level tools, each caged to a subset of the base tools and
	// gated exactly like the model's own calls. Their credentials are bound host-side on the injector.
	if err := installPlugins(dir, base, toolset, h.Secrets); err != nil {
		return nil, fmt.Errorf("workspace %q: plugins: %w", name, err)
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
func buildTools(base agentkit.ToolSet, llm agentkit.LLM, agents agent.Set) (agentkit.ToolSet, error) {
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

// installPlugins discovers the plugins under <dir>/plugins and folds each one's tools into the
// workspace toolset (as top-level <plugin>_<tool> tools, refusing a name collision), then binds its
// declared credentials host-side on the injector under the plugin's owner. A plugin's guest can only
// dispatch to the base tools its manifest lists — its cage — and every action it takes is gated the
// same way the model's own calls are. A credential value lives in the vault under the (lowercased)
// credential name; a missing value simply means the plugin runs unauthenticated.
func installPlugins(dir string, base, toolset agentkit.ToolSet, inj *secret.Injector) error {
	plugins, err := plugin.LoadAll(filepath.Join(dir, "plugins"), base)
	if err != nil {
		return err
	}
	for _, p := range plugins {
		pts, err := p.Tools()
		if err != nil {
			return err
		}
		for _, t := range pts {
			n := t.Spec().Name
			if _, dup := toolset[n]; dup {
				return fmt.Errorf("plugin %q tool %q collides with an existing tool", p.Name(), n)
			}
			toolset[n] = t
		}
		if inj == nil {
			continue
		}
		owner := plugin.Owner(p.Name())
		for _, c := range p.Credentials() {
			inj.AddBinding(owner, secret.Binding{
				Secret: strings.ToLower(c.Name),
				Host:   c.Host,
				Header: c.Header,
				Prefix: c.Prefix,
			})
		}
	}
	return nil
}

// policy is the workspace-root policy: the net kind asks the human (remembered for the session);
// every other Kind runs free. Per-agent policies (stricter) come with the agent slice.
func policy() gate.Policy {
	return gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		switch a.Kind {
		case tools.NetKind:
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
