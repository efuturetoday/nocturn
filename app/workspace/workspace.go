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
	Active   func() bool    // any device in the foreground, for routing proactive messages; nil = none
	Log      *slog.Logger
}

// Workspace is one isolated stack: its own tools, grants, persona, and chats over the Host.
type Workspace struct {
	name          string
	dir           string
	llm           agentkit.LLM
	tools         agentkit.ToolSet
	grants        gate.Grants
	approver      gate.Approver // the out-of-band human; handed to a guarded agent's firing, nil-safe
	chats         *chat.Manager // user chats
	agentChats    *chat.Manager // agent runs (agent-store counterpart of chats)
	userStore     *chat.Store   // the user chat store (behind chats)
	agentStore    *chat.Store   // agent run transcripts (SourceAgent, behind agentChats)
	agents        agent.Set
	agentRuntimes map[string]*runtime.Runtime // one per declared agent (cage+gate+autonomy), by name
	readOnly      *runtime.Runtime            // orphaned-run runtime: no tools, view an old transcript only
	sched         *agent.Scheduler
	reminders     *tools.Reminders // persistent reminder timers
	notify        *notifier        // the seam every proactive message leaves through
	waker         *tools.Waker     // self-continuation timers, bound to the chat manager
	vault         *secret.Vault    // this workspace's own encrypted credential vault; nil when locked
	log           *slog.Logger
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

	// One diagnostics collector drains every kind's discovery — agents, skills, plugins, MCP all feed
	// their skipped/shadowed items here, and it is logged once below. A malformed item is skipped
	// (fail-closed: its authority is simply absent), never fatal.
	var diag agentkit.Diagnostics

	agents := agent.Discover(filepath.Join(dir, "agents"), &diag)

	// wake lets a chat schedule its own continuation. One Waker per workspace, folded into the base
	// tools by Base; it is bound to the chat manager below (Bind) so a fired wake resolves the
	// invoking chat by id.
	waker := tools.NewWaker(tools.WithWakeLogger(h.Log))
	// Every proactive message out of THIS workspace passes through here: it gets the workspace name
	// stamped on, then routes by presence — the live connection when a device is watching, a push
	// when none is.
	notify := &notifier{ws: name, next: h.Notifier, active: h.Active}

	baseTools, err := tools.Base(tools.Config{Secrets: injector, Scanner: scanner, Root: mnt, Notifier: notify, Waker: waker, Logger: wslog.With("component", "tool")})
	if err != nil {
		return nil, fmt.Errorf("workspace %q: tools: %w", name, err)
	}
	// Reminders are persistent per-workspace and always offered: a fire reaches an awake device
	// through the notifier's observer even when no out-of-band sender is configured, so they no
	// longer depend on one existing. Their tools fold into the base set like any other; the instance
	// is held for its timer lifecycle (Restore below, Cancel on shutdown).
	reminders := tools.NewReminders(filepath.Join(dir, "reminders.json"), notify, scanner)
	reminders.SetLogger(wslog.With("component", "remind"))
	remindTools, err := reminders.Tools()
	if err != nil {
		return nil, fmt.Errorf("workspace %q: reminders: %w", name, err)
	}
	baseTools = append(baseTools, remindTools...)
	// Skills: load the workspace's skills/ folder into an agentkit.SkillSet. agentkit surfaces the
	// catalog (system prompt) and skill_load per top-level session; skill_read (for a skill's bundled
	// files) is a base tool, so it flows into the cages like the file tools. An invalid skill is
	// skipped inside Load with a logged warning — never blocks the workspace.
	skills, skillDirs := skill.Discover(filepath.Join(dir, "skills"), &diag)
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
	nPlugins, err := installPlugins(dir, base, toolset, injector, &diag)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: plugins: %w", name, err)
	}

	// Discover + connect the remote MCP servers declared in <dir>/mcp/*.json and fold their tools in
	// (as <server>_<tool>), each gated on the net host-allowlist like http_read/http_write (ADR-9).
	nMCP := installMCP(dir, toolset, injector, scanner, &diag, wslog.With("component", "mcp"))

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
		name:     name,
		dir:      dir,
		llm:      h.LLM,
		tools:    toolset,
		grants:   gs,
		approver: h.Approver,
		// User chats all spin under the one workspace root runtime — the resolver is a constant.
		chats:      chat.NewManager(func(string) *runtime.Runtime { return rt }, userStore, wslog),
		userStore:  userStore,
		agentStore: agentStore,
		agents:     agents,
		reminders:  reminders,
		notify:     notify,
		waker:      waker,
		vault:      vault,
		log:        wslog,
	}
	// One static runtime per declared agent (its cage + gate + autonomy), built once — Autonomy is a
	// declaration property, so a run's authority never depends on when it fires. The agent manager's
	// resolver maps a run to its owner's runtime via the persisted Meta.Agent; a run whose agent was
	// deleted resolves to readOnly (no tools) so its transcript still opens but it cannot act.
	w.agentRuntimes = make(map[string]*runtime.Runtime, len(agents))
	for _, a := range agents.All() {
		w.agentRuntimes[a.Name] = w.agentRuntime(a)
	}
	w.readOnly = runtime.New(h.LLM, runtime.WithGate(policy(), gs, nil))
	w.agentChats = chat.NewManager(func(id string) *runtime.Runtime {
		if art, ok := w.agentRuntimes[w.agentStore.OwnerOf(id)]; ok {
			return art
		}
		return w.readOnly
	}, agentStore, wslog)
	// Bind wake to BOTH managers: a fired wake resumes its chat by id in the store that owns it — an
	// agent run's continuation must re-open in the agent manager (its own runtime + store), not spawn
	// a stray user chat under the same id.
	waker.Bind(sessionRouter{user: w.chats, agent: w.agentChats, agentStore: agentStore})
	w.sched = agent.NewScheduler(agents, wslog, func(ctx context.Context, a agent.Agent) {
		// A scheduled firing is fire-and-forget; the run streams + persists like any chat. Surface only
		// a start-time rejection (unknown agent / shutting down) — the run's own errors land in its
		// transcript.
		if _, err := w.FireAgent(ctx, a.Name, "Run your scheduled task now."); err != nil {
			wslog.With("component", "scheduler").Error("scheduled agent failed", "agent", a.Name, "err", err)
		}
	})
	// Re-arm persisted reminders (overdue ones fire promptly) now the workspace is wired.
	reminders.Restore()
	// Every kind's discovery skips (bad agent/skill/plugin/server) drained through the one collector,
	// logged uniformly here — a single place an operator scans for what did NOT load and why.
	for _, d := range diag.All() {
		wslog.With("component", "discovery").Warn("skipped", "subject", d.Subject, "detail", d.Message)
	}
	// One readiness line stating what the workspace discovered — so an operator sees the assembled
	// stack (agents/skills/plugins/tools) at a glance instead of inferring it from behavior.
	wslog.With("component", "workspace").Info("workspace opened",
		"agents", len(agents), "skills", len(skillDirs), "plugins", nPlugins, "mcp", nMCP,
		"tools", len(toolset), "skipped", diag.Len())
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

// Reminders returns this workspace's pending reminders, soonest first — what a companion app lists.
func (w *Workspace) Reminders() []tools.Reminder { return w.reminders.List() }

// CancelReminder drops a pending reminder, reporting whether it existed. This is the app's cancel;
// the model has its own remind_cancel tool.
func (w *Workspace) CancelReminder(id string) bool { return w.reminders.CancelByID(id) }

// OnReminderChange wires a callback fired whenever the pending reminder set changes (a create, a
// cancel, a fire), for pushing a refresh to connected devices. Set once, at wiring time.
func (w *Workspace) OnReminderChange(fn func()) { w.reminders.OnChange(fn) }

// OnNotification wires a callback fired for every proactive message leaving this workspace (a notify
// tool call, a reminder firing), so a device that is already awake receives it over its live
// connection instead of relying on an out-of-band push it may never see. It runs BEFORE the push and
// must not block. Set once, at wiring time.
func (w *Workspace) OnNotification(fn func(tools.Notification)) { w.notify.observe(fn) }

// MarkRead advances a chat's shared read cursor in the kind-selected store ("agent" → agent runs,
// else user chats). The caller (the wire) always names the store, so this touches exactly one.
func (w *Workspace) MarkRead(kind, id string) {
	if kind == "agent" {
		_ = w.agentStore.MarkRead(id)
		return
	}
	_ = w.userStore.MarkRead(id)
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
func installPlugins(dir string, base, toolset agentkit.ToolSet, inj *secret.Injector, diag *agentkit.Diagnostics) (int, error) {
	plugins := plugin.Discover(filepath.Join(dir, "plugins"), base, diag)
	for _, p := range plugins.All() {
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
	}
	return len(plugins), nil
}

// mcpSetupTimeout bounds the startup handshake + tools/list for one MCP server.
const mcpSetupTimeout = 30 * time.Second

// installMCP connects the remote MCP servers declared in <dir>/mcp/*.json and folds each server's
// tools into the workspace toolset (as <server>_<tool>, refusing a name collision). Discovery
// (Connect + tools/list) runs on a bounded context with NO gate machinery installed — so the
// startup handshake never prompts; the runtime chat turn installs the gate, so a later
// model-invoked MCP call asks the human on the net axis exactly like http_read/http_write. A server
// that fails to load/connect/list is logged and skipped, never bricking the workspace (like a flaky
// plugin). Credentials are token (a bearer the operator seeded in the vault under mcp.SecretName)
// or public; interactive credential entry and OAuth wiring are a later slice.
func installMCP(dir string, toolset agentkit.ToolSet, inj *secret.Injector, scanner *secret.Scanner, diag *agentkit.Diagnostics, log *slog.Logger) int {
	servers := mcp.Discover(filepath.Join(dir, "mcp"), diag)
	installed := 0
	for _, srv := range servers.All() {
		conn, err := mcp.NewConn(srv, inj, scanner)
		if err != nil {
			log.Warn("mcp server skipped (bad config)", "server", srv.Name, "err", err)
			continue
		}
		conn.SetLogger(log)
		ctx, cancel := context.WithTimeout(context.Background(), mcpSetupTimeout)
		mtools, err := connectMCP(ctx, conn)
		cancel()
		if err != nil {
			var needAuth *mcp.AuthRequiredError
			if errors.As(err, &needAuth) {
				// The server wants OAuth and isn't authorized yet — not a failure, an action for the
				// operator. The daemon cannot open a browser; the interactive flow is the CLI.
				log.Info("mcp server needs authorization", "server", srv.Name, "action", "run: nocturn auth "+srv.Name)
			} else {
				log.Warn("mcp server skipped", "server", srv.Name, "err", err)
			}
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
	return installed
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
