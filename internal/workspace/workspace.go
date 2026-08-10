// Package workspace is the composition root per workspace: it bundles the tools (the cage), the
// permission gate (policy + durable grants + approver), the persona, and the chat store and manager
// into one aggregate over a shared Host. The daemon opens every workspace under its root; the
// terminal attaches to one.
//
// A workspace has two halves, and knowing which is which explains the file layout:
//
//   - DURABLE — built once by Open, never rebuilt, because there may only ever be one of it. One
//     vault handle on vault.enc, one timer per reminder and per wake, one chat store, one knowledge
//     index. workspace.go holds the type and Open; lifecycle.go holds what starts and stops around
//     it; secrets.go, grantstore.go, notifier.go, voice.go, mcpauth.go hold the pieces.
//   - DERIVED — everything discovery decides: agents, skills, plugins, MCP servers, the toolset they
//     add up to and the runtimes over it. It lives in snapshot.go and is replaced WHOLE by Reload,
//     which is why a workspace can gain a skill or an MCP server without a restart and without
//     cancelling a running turn. tools.go, plugins.go, mcp.go and prompt.go are what it builds from.
//
// policy.go is deliberately its own file: it is the permission rule, and tightening it is a decision
// rather than a bugfix.
package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/knowledge"
	"github.com/efuturetoday/nocturn/internal/knowledge/embed"
	"github.com/efuturetoday/nocturn/internal/memory"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/speaker"
	"github.com/efuturetoday/nocturn/internal/tools"
	"github.com/efuturetoday/nocturn/internal/voice"
)

const turnTimeout = 2 * time.Minute

// DefaultWorkspace is the workspace a fresh install and the terminal always have.
const DefaultWorkspace = "main"

// Host is the process-wide wiring shared by every workspace: the LLM endpoint and the one human
// approver (one device). It grows as more shared services arrive (notify, log, master key).
type Host struct {
	LLM      agentkit.LLM
	Live     agentkit.LiveLLM // duplex speech; nil = no spoken sessions in this process
	Approver gate.Approver
	Master   *secret.Master // root of the per-workspace vault keys (one passphrase); nil = vaults locked
	Notifier tools.Notifier // out-of-band user notification for the notify tool; nil = no notify
	Active   func() bool    // any device in the foreground, for routing proactive messages; nil = none
	// Speaker is the voice-embedding model this process loaded; nil = it cannot recognise anybody,
	// which is the normal case (the terminal chat has no microphone at all). It decides whether the
	// whoami tool exists — see Open.
	Speaker *speaker.Embedder
	// Embed says where to turn text into vectors. Unconfigured = this process cannot search
	// documents, and knowledge_search then does not exist rather than existing and failing.
	Embed embed.Config
	Log   *slog.Logger
}

// Workspace is one isolated stack: its own tools, grants, persona, and chats over the Host.
//
// It has two halves, and the line between them is the one question that decides everything about
// reloading: WHAT MAY NOT EXIST TWICE. The fields below are the durable half — one vault handle, one
// reminder timer set, one chat store, one knowledge index — built once by Open and never rebuilt.
// Everything discovery derives lives in `cur` and is replaced whole (see snapshot.go).
type Workspace struct {
	name       string
	dir        string
	llm        agentkit.LLM
	grants     gate.Grants
	approver   gate.Approver     // the out-of-band human; handed to a guarded agent's firing, nil-safe
	chats      *chat.Manager     // user chats
	agentChats *chat.Manager     // agent runs (agent-store counterpart of chats)
	userStore  *chat.Store       // the user chat store (behind chats)
	agentStore *chat.Store       // agent run transcripts (SourceAgent, behind agentChats)
	reminders  *tools.Reminders  // persistent reminder timers
	notify     *notifier         // the seam every proactive message leaves through
	waker      *tools.Waker      // self-continuation timers, bound to the chat manager
	mem        *memory.Store     // the assistant's durable notes; its index is folded into every prompt
	knowledge  *knowledge.Store  // the user's own documents; nil when no embedder is configured
	voice      *voice.Manager    // live spoken sessions, one per device; nil when Host.Live is unset
	voices     *speaker.Profiles // enrolled voices, for telling this household apart
	accounts   *MCPAuth          // MCP OAuth session orchestration; nil when the vault is locked
	log        *slog.Logger

	// title is the display name, separate from name because name is identity — see meta.go.
	title title

	// The credential stack. It is durable — one vault on one file — and internal/secret is built to
	// be reconciled rather than rebuilt: the Injector's own doc says bindings and resolvers are
	// mutated at runtime, and RemoveBindingsFor exists for exactly the uninstall case. So each discovery pass
	// re-runs the discovery-dependent registrations into these, instead of replacing them.
	sec workspaceSecrets

	// baseTools is every base tool that discovery does NOT decide — the file/net/notify/wake set plus
	// reminders, memory, knowledge and whoami, each belonging to a durable object above. Each pass
	// clones it and appends the one tool discovery does decide, skill_read.
	baseTools []agentkit.Tool

	// cur is the derived half: agents, skills, plugins, MCP, the toolset and the runtimes. Published
	// with one atomic store so a reader sees all of one snapshot or all of the other, never a mix —
	// which is what an inventory read racing a reload used to be. reloadMu makes discover+publish
	// single-flight, so two devices installing at once queue instead of interleaving.
	cur      atomic.Pointer[snapshot]
	reloadMu sync.Mutex

	// The root chat runtime and the no-tools fallback. Both are durable: the root one reads the
	// current snapshot per turn rather than being rebuilt with it, which is what lets a chat pick up a
	// newly installed skill in its next message without reopening anything.
	rt       *runtime.Runtime
	readOnly *runtime.Runtime

	// retired holds the plugins of assemblies this workspace has replaced, closed at Close.
	//
	// A KindWASM plugin compiles its own wazero guest LAZILY, on first call — so a fresh snapshot
	// holds nothing, and only a plugin actually used under a superseded snapshot has anything to
	// release. Closing those at reload time would mean guessing when the last call under the old set
	// finished; keeping them until the workspace closes costs one compiled guest per (reload, plugin
	// actually called) and needs no guess at all. KindJS plugins share the process-wide QuickJS
	// engine and hold nothing either way.
	retiredMu sync.Mutex
	retired   []*plugin.Plugin

	// stopBg cancels the background work StartAgents owns — the cron scheduler and the document
	// reconcile — so Close can stop them. A cancel func rather than the context it came from: a
	// context stored in a struct is the thing to avoid, a shutdown handle is not one. bgClosed
	// latches so a StartAgents that has not registered yet does not outlive the Close that raced it.
	// reloaded is closed and replaced by a reload, which is how the scheduler picks up a changed
	// agent set without anybody holding a context to re-aim.
	bgMu     sync.Mutex
	bgClosed bool
	stopBg   context.CancelFunc
	reloaded chan struct{}
}

// path joins a control-plane path under the workspace root.
func (w *Workspace) path(sub string) string { return filepath.Join(w.dir, sub) }

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
	sec, err := buildWorkspaceSecrets(h.Master, dir, name, wslog)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: secrets: %w", name, err)
	}
	injector, scanner := sec.injector, sec.scanner

	// wake lets a chat schedule its own continuation. One Waker per workspace, folded into the base
	// tools by Base; it is bound to the chat manager below (Bind) so a fired wake resolves the
	// invoking chat by id. Its store is a control-plane file beside reminders.json — a pending
	// continuation is state of the same kind, and losing it across a restart was invisible.
	waker := tools.NewWaker(
		tools.WithWakeLogger(h.Log),
		tools.WithWakeStore(filepath.Join(dir, "wakes.json")),
	)
	// Every proactive message out of THIS workspace passes through here: it gets the workspace name
	// stamped on, then routes by presence — the live connection when a device is watching, a push
	// when none is.
	notify := &notifier{ws: name, next: h.Notifier, active: h.Active}

	baseTools, err := tools.Base(tools.Config{
		Secrets:  injector,
		Scanner:  scanner,
		Root:     mnt,
		Notifier: notify,
		Waker:    waker,
		Logger:   wslog.With("component", "tool"),
	})
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
	// Memory: the assistant's own durable notes. Its folder is a SIBLING of mnt, so the file tools
	// cannot reach it — memory_write is the only writer and it asks the human. The tools join the base
	// set, so they flow into the agent cages exactly like skill_read; the index is folded into the
	// system prompt below.
	memDir := filepath.Join(dir, "memory")
	// Created eagerly: os.OpenRoot needs the directory to exist, so the FIRST memory_write would
	// otherwise fail on a fresh workspace. It also gives the human an obvious place to look.
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace %q: memory dir: %w", name, err)
	}
	mem := memory.New(memDir, scanner)
	mem.SetLogger(wslog.With("component", "memory"))
	memTools, err := mem.Tools()
	if err != nil {
		return nil, fmt.Errorf("workspace %q: memory: %w", name, err)
	}
	baseTools = append(baseTools, memTools...)

	// Knowledge: the user's OWN documents, and the mirror image of memory. It lives INSIDE mnt,
	// because documents are data the human puts there and the model may read and write like any other
	// file — the same argument ADR-10 makes about a self-written skill granting no authority. The
	// index does not: it is host state, and sits beside grants.json where no file tool reaches it.
	//
	// Absent when no embedder is configured. The tool then does not exist, rather than existing and
	// failing on every call.
	var knowledgeStore *knowledge.Store
	if h.Embed.Configured() {
		kdir := filepath.Join(mnt, "knowledge")
		if err := os.MkdirAll(kdir, 0o700); err != nil {
			return nil, fmt.Errorf("workspace %q: knowledge dir: %w", name, err)
		}
		klog := wslog.With("component", "knowledge")
		knowledgeStore, err = knowledge.New(knowledge.Options{
			Dir:       kdir,
			IndexPath: filepath.Join(dir, "knowledge.idx.json"),
			// One client per workspace rather than per process, because the egress scanner it sends
			// through belongs to THIS workspace's vault.
			Embedder: embed.New(h.Embed, scanner),
			Scanner:  scanner,
			Log:      klog,
		})
		if err != nil {
			return nil, fmt.Errorf("workspace %q: knowledge: %w", name, err)
		}
		ktools, err := knowledgeStore.Tools()
		if err != nil {
			return nil, fmt.Errorf("workspace %q: knowledge tools: %w", name, err)
		}
		baseTools = append(baseTools, ktools...)
	}

	// Ungated, and reaching nothing: it reports what the microphone already established, which the
	// model could equally have asked the person.
	//
	// It exists only where recognition does. Without a model loaded, whoami cannot answer anything
	// but "unknown" for the life of the process — and a tool that is structurally incapable of a
	// result is worse than a missing one: it costs a slot in every prompt, and it invites the model
	// to ask a question whose answer is always no. So the terminal chat, which has no microphone at
	// all, simply does not have it.
	if h.Speaker != nil {
		whoami, err := speaker.WhoAmI()
		if err != nil {
			return nil, fmt.Errorf("workspace %q: whoami: %w", name, err)
		}
		baseTools = append(baseTools, whoami)
	}

	// Control plane, beside the grants: a voiceprint is not something a file tool should reach.
	voices, err := speaker.OpenProfiles(filepath.Join(dir, "voices.json"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: %w", name, err)
	}

	gs, err := newGrantStore(filepath.Join(dir, "grants.json"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: grants: %w", name, err)
	}

	userStore, err := chat.NewStore(filepath.Join(dir, "chats"))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: chat store: %w", name, err)
	}
	agentStore, err := chat.NewStore(filepath.Join(dir, "agent-runs"), chat.WithSource(chat.SourceAgent))
	if err != nil {
		return nil, fmt.Errorf("workspace %q: agent store: %w", name, err)
	}

	w := &Workspace{
		voices:     voices,
		knowledge:  knowledgeStore,
		name:       name,
		dir:        dir,
		llm:        h.LLM,
		grants:     gs,
		approver:   h.Approver,
		userStore:  userStore,
		agentStore: agentStore,
		reminders:  reminders,
		notify:     notify,
		waker:      waker,
		mem:        mem,
		log:        wslog,
		sec:        sec,
		baseTools:  baseTools,
		reloaded:   make(chan struct{}),
	}

	w.title.set(readMeta(dir).Title)

	// The first snapshot: discovery, the toolset, the per-agent runtimes. Everything above is durable and stays
	// for the life of the workspace; everything this builds can be rebuilt at any time. It comes
	// before the consumers below because they read through w.snapshot() — a voice driver caging
	// itself, for one, does so the moment it is built.
	if err := w.Reload(); err != nil {
		return nil, err
	}

	// The root runtime is DURABLE and asks the current snapshot for its tools, skills and persona once
	// per turn. That is the whole reload story for a user chat: install a skill mid-conversation and
	// the very next message has it, with no session to reopen and no turn interrupted — a turn is
	// handed one set at its start and works with it throughout, so a tool can never vanish between two
	// calls the model already planned together.
	w.rt = runtime.New(h.LLM,
		runtime.WithToolsFunc(func() agentkit.ToolSet { return w.snapshot().tools }),
		runtime.WithSkillsFunc(func() agentkit.SkillSet { return w.snapshot().skills }),
		runtime.WithGate(policy(), gs, h.Approver),
		runtime.WithGateLogger(agentkit.SlogLogger(wslog)), // trace gate allow/deny/ask with ws+chat
		runtime.WithSession(
			agentkit.WithSystemFunc(func() string {
				a := w.snapshot()
				return composePrompt(a.persona, w.mem, hasTool(a.tools))
			}),
			agentkit.WithTimeout(turnTimeout),
			agentkit.WithLogger(agentkit.SlogLogger(wslog)),
		),
	)
	// The runtime a run whose agent was deleted falls back to: no tools, so its transcript still opens
	// but it cannot act. Durable because it has nothing discovery could change.
	w.readOnly = runtime.New(h.LLM, runtime.WithGate(policy(), gs, nil))

	// User chats all spin under that one runtime — the resolver is a constant again.
	w.chats = chat.NewManager(func(string) *runtime.Runtime { return w.rt }, userStore, wslog)
	// An agent run spins under its OWNING agent's runtime, resolved from the persisted Meta.Agent at
	// the moment the run opens — and unlike a chat it KEEPS that one. See snapshot.agentRuntimes.
	w.agentChats = chat.NewManager(func(id string) *runtime.Runtime {
		if art, ok := w.snapshot().agentRuntimes[w.agentStore.OwnerOf(id)]; ok {
			return art
		}
		return w.readOnly
	}, agentStore, wslog)

	// The MCP OAuth orchestrator shares the master + this workspace's shard routing, so an account a
	// device connects over the WebSocket lands in the same folder shard the daemon reads at boot. Only
	// when unlocked — a locked vault cannot store a token, so there is nothing to orchestrate.
	if h.Master != nil {
		w.accounts = NewMCPAuth(h.Master, dir, name)
	}
	// Bind wake to BOTH managers: a fired wake resumes its chat by id in the store that owns it — an
	// agent run's continuation must re-open in the agent manager (its own runtime + store), not spawn
	// a stray user chat under the same id.
	waker.Bind(sessionRouter{user: w.chats, agent: w.agentChats, agentStore: agentStore})
	w.startVoice(h.Live, h.Approver)
	// Bound any existing agent-run backlog (a long-running cron agent accumulates thousands otherwise);
	// FireAgent keeps each agent capped from here.
	w.pruneAgentRuns()
	// Re-arm persisted reminders and wakes (overdue ones fire promptly) now the workspace is wired.
	// Both come AFTER waker.Bind above: an overdue wake fires the moment its timer is armed, and
	// firing consumes it, so arming before the lookup seam exists would drop exactly what the store
	// is there to keep.
	reminders.Restore()
	waker.Restore()
	return w, nil
}

// SkillsDir is where this workspace's skills live. Exported because managing them — listing, reading,
// switching one off — is the consumer's job, the same way discovery is: internal/skill owns the
// format and the rules, the workspace owns where they sit.
func (w *Workspace) SkillsDir() string { return w.path("skills") }

// Name returns the workspace name.
func (w *Workspace) Name() string { return w.name }

// Accounts is the workspace's MCP OAuth orchestrator (Begin/Complete/List), or nil when the vault is
// locked (no master passphrase — a token cannot be stored, so there is nothing to connect). The
// daemon's WebSocket auth handler drives it for the companion app.
func (w *Workspace) Accounts() *MCPAuth { return w.accounts }

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
