// Package workspace is the composition aggregate: the one place that OWNS everything
// a workspace can do — its guard, its toolset, its skills catalog, its child agents,
// its scheduler, its durable grants — assembled in one place instead of smeared across
// a bootstrap function. It is also the ONLY chat.Charter factory: RootCharter mints
// the interactive chat's spec, AgentCharter compiles an agent declaration into its
// own; every chat operates over the workspace's concrete parts, nothing is assembled
// ad hoc outside it.
//
// The process-wide shared services (the LLM connection, the approval engine, the master
// key, the notify channel) live in Host and are handed to Open — those are genuinely
// one-per-process; everything else is per-workspace and owned here.
package workspace

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/filecap"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/grantstore"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/notifycap"
	"github.com/efuturetoday/nocturn/internal/persona"
	"github.com/efuturetoday/nocturn/internal/remindcap"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/timecap"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Host is the process-wide machinery every workspace is built against — one per
// process, not per workspace. Fields are named by role (Approvals, not Engine).
type Host struct {
	Master    *secret.Master   // derives each workspace's vault key
	Approvals *hitl.Engine     // the out-of-band approval engine (workspace-agnostic)
	Model     brain.Model      // the shared LLM connection
	Notify    notifycap.Pusher // out-of-band push, or an attended fallback
	Log       *slog.Logger     // diagnostic logger (shared across workspaces), or nil
}

// Vault unlocks a workspace's credentials vault. The composition root supplies it (the
// unlock ceremony — passphrase/age — lives at the boundary, not in this package).
type Vault func(dir, name string) (*secret.Vault, error)

// Workspace owns one workspace's composition. Every field is named by ROLE, not by its
// type (CLAUDE.md §6): a guard, a loop, a credentials injector, a leak scanner.
type Workspace struct {
	name, dir string

	guard       *gateway.Guard    // guard + HITL: every effect passes through it
	tools       *tool.Registry    // the model-facing toolset
	skills      *skill.Index      // the skill catalog
	agents      []agent.Agent     // child-agent declarations
	grants      *grantstore.Store // durable "always" consent for the interactive session
	loop        *brain.Brain      // the shared model loop
	reminders   *remindcap.Reminders
	secrets     *secret.Vault    // the credentials vault
	credentials *secret.Injector // host-side credentials injection
	leakScanner *secret.Scanner  // bidirectional secret leak scan

	persona *persona.Store // the assistant's system prompt — its own state service (see Persona/SetPersona)
}

// toolTimeout bounds a single tool call inside the loop.
const toolTimeout = 20 * time.Second

// scriptTimeout bounds a whole code.run evaluation.
const scriptTimeout = 60 * time.Second

// httpTimeout bounds a single outbound HTTP effect.
const httpTimeout = 15 * time.Second

// approvalTTL is how long an out-of-band approval stays answerable.
const approvalTTL = 2 * time.Minute

// notifyRatePerMin / remindRatePerMin bound how often the assistant may reach the user's
// device per minute — anti-spam for the silent, base-Allow families (a runaway loop can't
// buzz the phone endlessly). Other families are left unconfigured (unlimited).
const (
	notifyRatePerMin = 10
	remindRatePerMin = 20
)

// Open assembles one workspace over the shared Host: it unlocks the vault, builds the
// guard, the built-in tool providers (net/file/time/notify/remind + the code.run
// interpreter), discovers the skill catalog, loads the child agents, and wires the
// shared loop and durable grants. Interactive extensions (plugins, MCP, OAuth) and the
// UI transport (activity sink, self-wake, scheduler log) are wired by the caller over
// the exposed parts — this package stays free of stdin/stdout and any transport.
func Open(h Host, base func() capability.Policy, unlock Vault, name, dir string) (*Workspace, error) {
	vault, err := unlock(dir, name)
	if err != nil {
		return nil, err
	}

	store := vault.Store()
	credentials := secret.NewInjector(store)
	leakScanner := secret.NewScanner(store)

	// Rate cap: only the user-facing, silently-allowed families are limited (anti-spam) —
	// reads stay unconfigured hence unlimited, so a script doing 100 file.reads is fine.
	// This also makes the grant/autonomy rate cap in Authorize real for these families.
	rate := capability.NewRateLimiter(
		capability.WithLimit("notify", notifyRatePerMin, time.Minute),
		capability.WithLimit("remind", remindRatePerMin, time.Minute),
	)
	guard := &gateway.Guard{Policy: base(), Approvals: h.Approvals, TTL: approvalTTL, Rate: rate, Log: h.Log}

	net := netcap.New(guard, netcap.WithCredentials(credentials), netcap.WithScanner(leakScanner), netcap.WithTimeout(httpTimeout))
	files := filecap.New(guard, filepath.Join(dir, "mnt"))
	notify := notifycap.New(guard, h.Notify, leakScanner)
	clock := timecap.New()
	reminders := remindcap.New(guard, remindcap.LoadStore(filepath.Join(dir, "reminders.json")), h.Notify, leakScanner)
	reminders.Restore()

	tools := tool.NewRegistry()
	tools.AddMany(net.Tools()...)
	tools.AddMany(files.Tools()...)
	tools.AddMany(clock.Tools()...)
	tools.AddMany(notify.Tools()...)
	tools.AddMany(reminders.Tools()...)

	interp := script.New(tools)
	interp.Timeout = scriptTimeout
	tools.Add(interp.Tool())

	skillsFolder := filepath.Join(dir, "skills")
	skills := skill.Discover([]skill.Scope{{Dir: skillsFolder, Location: "workspace"}})
	if lt, ok := skills.LoadTool(); ok {
		tools.Add(lt)
	}
	if skills.Len() > 0 {
		tools.Add(skills.ReadTool())
	}

	agentsFolder := filepath.Join(dir, "agents")
	agents, err := agent.Discover(agentsFolder)
	if err != nil {
		return nil, err
	}

	grants := grantstore.Load(filepath.Join(dir, "grants.json"))
	loop := brain.New(h.Model, brain.WithToolTimeout(toolTimeout))

	return &Workspace{
		name: name, dir: dir,
		guard:       guard,
		tools:       tools,
		skills:      skills,
		agents:      agents,
		grants:      grants,
		loop:        loop,
		reminders:   reminders,
		secrets:     vault,
		credentials: credentials,
		leakScanner: leakScanner,
		persona:     persona.Load(dir),
	}, nil
}

// Persona returns the workspace's current system prompt (state, not the loader detail).
func (w *Workspace) Persona() string { return w.persona.Get() }

// SetPersona persists a new persona; the persona service owns the write, the layering, and
// its own synchronization. Callers deal in state, never the file or a lock.
func (w *Workspace) SetPersona(text string) error { return w.persona.Set(text) }

// RootCharter mints the construction spec for this workspace's ROOT chat: the full
// toolset, the current persona (read live, so a chat opened after a persona change
// seeds the new one), and an unrestricted authority backed by the workspace's durable
// grants. The workspace is the only Charter factory — a chat.Manager receives this as
// its Root factory and stays agent-agnostic.
func (w *Workspace) RootCharter() chat.Charter {
	return chat.Charter{
		Tools:     w.tools,
		System:    w.Persona(),
		Authority: gateway.Authority{Grants: w.grants},
	}
}

// AgentCharter compiles the named agent's declaration into a chat.Charter: its
// FILTERED tool subset (a tool outside its list is unreachable, not merely hidden),
// its Instructions as the system prompt, and an Authority carrying its declared
// policy/cage tightenings, its OWN durable grants (per-agent file — they never
// cross-match another owner's), and a "<workspace>/<agent>" provenance label.
//
// autonomy is a property of the TRIGGER, not the declaration: pass
// capability.AutonomyAttended when a human is at the triggering surface (an
// in-chat /agent spawn, an app reopen — normal HITL applies), or the agent's
// declared dial for an unattended cron firing. An unknown name returns an error.
func (w *Workspace) AgentCharter(name string, autonomy capability.Autonomy) (chat.Charter, error) {
	for _, a := range w.agents {
		if a.Name != name {
			continue
		}
		return chat.Charter{
			Tools:  w.tools.Select(a.Matches),
			System: a.Instructions,
			Authority: gateway.Authority{
				Grants:   grantstore.Load(grantstore.Path(filepath.Join(w.dir, "agents"), name)),
				Policy:   a.Policy,
				Cage:     a.Cage,
				Autonomy: autonomy,
				Label:    w.name + "/" + name,
			},
			Budget: a.Budget,
		}, nil
	}
	return chat.Charter{}, fmt.Errorf("workspace %s: unknown agent %q", w.name, name)
}

// --- accessors the composition root uses to finish wiring (interactive extensions +
// UI transport). These shrink as more moves into the workspace. ---

// Name is the workspace's name.
func (w *Workspace) Name() string { return w.name }

// Dir is the workspace's root directory.
func (w *Workspace) Dir() string { return w.dir }

// Tools is the workspace's registry — extensions (plugins, MCP, self-wake) add to it.
func (w *Workspace) Tools() *tool.Registry { return w.tools }

// Guard is the workspace's authorization pipeline (for MCP wiring).
func (w *Workspace) Guard() *gateway.Guard { return w.guard }

// Brain is the shared model loop (for the chat manager to open sessions).
func (w *Workspace) Brain() *brain.Brain { return w.loop }

// Vault is the workspace's credentials vault (for plugin/MCP/OAuth wiring).
func (w *Workspace) Vault() *secret.Vault { return w.secrets }

// Credential is the host-side credentials injector (for plugin/MCP/OAuth wiring).
func (w *Workspace) Credentials() *secret.Injector { return w.credentials }

// LeakScan is the workspace's secret leak scanner (for MCP wiring).
func (w *Workspace) LeakScanner() *secret.Scanner { return w.leakScanner }

// Skills is the workspace's skill catalog (for the UI and startup notice).
func (w *Workspace) Skills() *skill.Index { return w.skills }

// Agents is the workspace's child-agent declarations (for the UI and scheduler).
func (w *Workspace) Agents() []agent.Agent { return w.agents }

// Reminders is the workspace's reminder capability (for the app to list pending reminders and
// for the composition root to wire a live-sync change hook).
func (w *Workspace) Reminders() *remindcap.Reminders { return w.reminders }
