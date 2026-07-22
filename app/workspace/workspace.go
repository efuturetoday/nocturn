// Package workspace is the composition root per workspace: it bundles the tools (the cage), the
// permission gate (policy + durable grants + approver), the persona, and the chat store and manager
// into one aggregate over a shared Host. app/main opens one; multiplexing several is a later slice.
package workspace

import (
	"context"
	"errors"
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
	"github.com/efuturetoday/nocturn/app/mcp"
	"github.com/efuturetoday/nocturn/app/plugin"
	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/skill"
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
	Master   *secret.Master // root of the per-workspace vault keys (one passphrase); nil = vaults locked
	Notifier tools.Notifier // out-of-band user notification for the notify tool; nil = no notify
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
	reminders  *tools.Reminders // persistent reminder timers; nil when no notifier
	waker      *tools.Waker     // self-continuation timers, bound to the chat manager
	vault      *secret.Vault    // this workspace's own encrypted credential vault; nil when locked
	log        *slog.Logger
}

// Open builds (creating its directory if needed) a workspace named name rooted at dir: it assembles
// the toolset, the durable grant store, the persona, a gated runtime, and a chat store + manager.
func Open(h Host, name, dir string) (*Workspace, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace %q: %w", name, err)
	}
	// The file tools mount ONLY dir/mnt — the LLM's world. The workspace root (dir) holds the
	// control plane (grants.json, reminders.json, agents/, PERSONA.md, chats/, agent-runs/, …); those
	// are siblings of the mount, so os.Root confinement keeps the model out of them. Rooting the file
	// tools at dir itself would let file_read/file_write reach grants.json — a full gate bypass.
	mnt := filepath.Join(dir, "mnt")
	if err := os.MkdirAll(mnt, 0o700); err != nil {
		return nil, fmt.Errorf("workspace %q: mnt: %w", name, err)
	}

	// One per-workspace logger with ws=name baked in, shared by the session loop, chat manager,
	// scheduler and fired agents — so every line from this workspace carries its identity (the chat
	// id is folded in per-turn by the ctxHandler on top of this). Kept component-less: the agentkit
	// session self-tags its lines (component=turn/tool/llm); app subsystems tag their own.
	wslog := h.Log.With("ws", name)

	// This workspace's OWN credential stack: its own encrypted vault (dir/vault.enc, keyed by the
	// master's per-workspace sub-key), injector, and scanner. Isolation is by construction — a token
	// authorized in another workspace is under a different key, file, and injector. A locked vault
	// (nil master) yields nil injector/scanner: the workspace runs without host-owned credentials.
	// The old global dataDir/vault.enc is orphaned by this move (greenfield; re-provision per ws).
	injector, scanner, vault, err := buildWorkspaceSecrets(h.Master, dir, name, wslog)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: secrets: %w", name, err)
	}

	agents, err := agent.Discover(filepath.Join(dir, "agents"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: agents: %w", name, err)
	}

	// wake lets a chat schedule its own continuation. One Waker per workspace, folded into the base
	// tools by Base; it is bound to the chat manager below (Bind) so a fired wake resolves the
	// invoking chat by id.
	waker := tools.NewWaker(tools.WithWakeLogger(h.Log))
	baseTools, err := tools.Base(tools.Config{Secrets: injector, Scanner: scanner, Root: mnt, Notifier: h.Notifier, Waker: waker})
	if err != nil {
		return nil, fmt.Errorf("workspace %q: tools: %w", name, err)
	}
	// Reminders are persistent per-workspace and fire through the notifier, so they only exist when a
	// notifier does. Their tools fold into the base set like any other; the instance is held for its
	// timer lifecycle (Restore below, Close on shutdown).
	var reminders *tools.Reminders
	if h.Notifier != nil {
		reminders = tools.NewReminders(filepath.Join(dir, "reminders.json"), h.Notifier, scanner)
		remindTools, err := reminders.Tools()
		if err != nil {
			return nil, fmt.Errorf("workspace %q: reminders: %w", name, err)
		}
		baseTools = append(baseTools, remindTools...)
	}
	// Skills: load the workspace's skills/ folder into an agentkit.SkillSet. agentkit surfaces the
	// catalog (system prompt) and skill_load per top-level session; skill_read (for a skill's bundled
	// files) is a base tool, so it flows into the cages like the file tools. An invalid skill is
	// skipped inside Load with a logged warning — never blocks the workspace.
	skills, skillDirs, err := skill.Load(filepath.Join(dir, "skills"), h.Log)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: skills: %w", name, err)
	}
	if len(skillDirs) > 0 {
		readTool, err := skill.ReadTool(skillDirs)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: skill_read: %w", name, err)
		}
		baseTools = append(baseTools, readTool)
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
	nPlugins, err := installPlugins(dir, base, toolset, injector, wslog.With("component", "plugin"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: plugins: %w", name, err)
	}

	// Discover + connect the remote MCP servers declared in <dir>/mcp.json and fold their tools in
	// (as <server>_<tool>), each gated on the net host-allowlist like http_read/http_write (ADR-9).
	nMCP, err := installMCP(dir, toolset, injector, scanner, wslog.With("component", "mcp"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: mcp: %w", name, err)
	}

	rt := runtime.New(h.LLM,
		runtime.WithTools(toolset),
		runtime.WithSkills(skills),
		runtime.WithGate(policy(), gs, h.Approver),
		runtime.WithGateLogger(agentkit.SlogLogger(wslog)), // trace gate allow/deny/ask with ws+chat
		runtime.WithSession(
			agentkit.WithSystem(resolvePersona(dir, h.Log)),
			agentkit.WithTimeout(turnTimeout),
			agentkit.WithLogger(agentkit.SlogLogger(wslog)),
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
		chats:      chat.NewManager(rt, userStore, wslog),
		userStore:  userStore,
		agentStore: agentStore,
		agents:     agents,
		reminders:  reminders,
		waker:      waker,
		vault:      vault,
		log:        wslog,
	}
	// Bind wake to the live chat manager now it exists: a fired wake resolves its chat by id via Open.
	waker.Bind(w.chats)
	w.sched = agent.NewScheduler(agents, wslog, func(ctx context.Context, a agent.Agent) {
		// A scheduled firing is unattended — nobody sees a returned error, so surface it here or it
		// vanishes. The answer is intentionally dropped (the transcript is persisted by FireAgent).
		if _, err := w.FireAgent(ctx, a.Name, "Run your scheduled task now."); err != nil {
			wslog.With("component", "scheduler").Error("scheduled agent failed", "agent", a.Name, "err", err)
		}
	})
	// Re-arm persisted reminders (overdue ones fire promptly) now the workspace is wired.
	if reminders != nil {
		reminders.Restore()
	}
	// One readiness line stating what the workspace discovered — so an operator sees the assembled
	// stack (agents/skills/plugins/tools) at a glance instead of inferring it from behavior. Per-item
	// detail (invalid skills, each plugin) is logged at Warn/Debug where it happens.
	wslog.With("component", "workspace").Info("workspace opened",
		"agents", len(agents), "skills", len(skillDirs), "plugins", nPlugins, "mcp", nMCP, "tools", len(toolset))
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
func installPlugins(dir string, base, toolset agentkit.ToolSet, inj *secret.Injector, log *slog.Logger) (int, error) {
	plugins, err := plugin.LoadAll(filepath.Join(dir, "plugins"), base)
	if err != nil {
		return 0, err
	}
	for _, p := range plugins {
		pts, err := p.Tools()
		if err != nil {
			return 0, err
		}
		for _, t := range pts {
			n := t.Spec().Name
			if _, dup := toolset[n]; dup {
				return 0, fmt.Errorf("plugin %q tool %q collides with an existing tool", p.Name(), n)
			}
			toolset[n] = t
		}
		if inj != nil {
			owner := plugin.Owner(p.Name())
			for _, c := range p.Credentials() {
				inj.AddBinding(owner, secret.Binding{
					Secret: plugin.SecretName(p.Name(), c.Name), // owner-namespaced: no cross-plugin key collision
					Host:   c.Host,
					Header: c.Header,
					Prefix: c.Prefix,
				})
			}
		}
		log.Debug("plugin installed", "plugin", p.Name(), "tools", len(pts))
	}
	return len(plugins), nil
}

// mcpSetupTimeout bounds the startup handshake + tools/list for one MCP server.
const mcpSetupTimeout = 30 * time.Second

// installMCP connects the remote MCP servers declared in <dir>/mcp.json and folds each server's
// tools into the workspace toolset (as <server>_<tool>, refusing a name collision). Discovery
// (Connect + tools/list) runs on a bounded context with NO gate machinery installed — so the
// startup handshake never prompts; the runtime chat turn installs the gate, so a later
// model-invoked MCP call asks the human on the net axis exactly like http_read/http_write. A server
// that fails to load/connect/list is logged and skipped, never bricking the workspace (like a flaky
// plugin). Credentials are token (a bearer the operator seeded in the vault under mcp.SecretName)
// or public; interactive credential entry and OAuth wiring are a later slice.
func installMCP(dir string, toolset agentkit.ToolSet, inj *secret.Injector, scanner *secret.Scanner, log *slog.Logger) (int, error) {
	servers, err := mcp.LoadConfig(filepath.Join(dir, "mcp.json"))
	if err != nil {
		return 0, err // a malformed control-plane config fails startup, like a bad plugin.json
	}
	installed := 0
	for _, srv := range servers {
		conn, err := mcp.NewConn(srv, inj, scanner)
		if err != nil {
			log.Warn("mcp server skipped (bad config)", "server", srv.Name, "err", err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), mcpSetupTimeout)
		mtools, err := connectMCP(ctx, conn)
		cancel()
		if err != nil {
			log.Warn("mcp server skipped", "server", srv.Name, "err", err)
			continue
		}
		clash := false
		for _, t := range mtools {
			if _, dup := toolset[t.Spec().Name]; dup {
				log.Warn("mcp tool name collides, skipping server", "server", srv.Name, "tool", t.Spec().Name)
				clash = true
				break
			}
		}
		if clash {
			continue
		}
		for _, t := range mtools {
			toolset[t.Spec().Name] = t
		}
		installed++
		log.Debug("mcp server connected", "server", srv.Name, "tools", len(mtools))
	}
	return installed, nil
}

// connectMCP performs one server's discovery (handshake + tools/list) on the setup ctx.
func connectMCP(ctx context.Context, conn *mcp.Conn) ([]agentkit.Tool, error) {
	if err := conn.Connect(ctx); err != nil {
		return nil, err
	}
	return conn.Tools(ctx)
}

// policy is the workspace-root policy: the net kind asks the human (remembered for the session);
// every other Kind runs free. Per-agent policies (stricter) come with the agent slice.
func policy() gate.Policy {
	return gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		switch a.Kind {
		case tools.NetKind, tools.FileKind:
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
func resolvePersona(dir string, log *slog.Logger) string {
	data, err := os.ReadFile(filepath.Join(dir, "PERSONA.md"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			// A real read error (permissions, I/O) on an existing PERSONA.md must not
			// silently swap in the wrong identity — surface it rather than mask it.
			log.Warn("workspace: reading PERSONA.md, using default", "dir", dir, "err", err)
		}
		return defaultPersona
	}
	if body := strings.TrimSpace(string(data)); body != "" {
		return body
	}
	return defaultPersona
}
