// Package workspace is the composition aggregate: the one place that OWNS everything
// a workspace can do — its guard, its toolset, its skills catalog, its child agents,
// its scheduler, its durable grants — assembled in one place instead of smeared across
// a bootstrap function. An interactive Session and a child-agent run are opened FROM a
// workspace and operate over its concrete parts; nothing is assembled ad hoc outside it.
//
// The process-wide shared services (the LLM connection, the approval engine, the master
// key, the notify channel) live in Host and are handed to Open — those are genuinely
// one-per-process; everything else is per-workspace and owned here.
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/filecap"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/grantstore"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/notifycap"
	"github.com/efuturetoday/nocturn/internal/remindcap"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/session"
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
	persona     string           // the interactive session's system prompt (PERSONA.md, layered)
}

// defaultPersona is the built-in system prompt used when no PERSONA.md is found at
// either layer. The lowest, always-present fallback of loadPersona.
const defaultPersona = "You are Nocturn, a careful assistant. " +
	"Use a tool when it helps; otherwise answer directly."

// loadPersona resolves the interactive session's system prompt with OVERRIDE semantics
// (first hit wins, never appended): the workspace's OWN PERSONA.md, else the shared
// PERSONA.md in the parent directory (workspaces/PERSONA.md), else the built-in default.
// PERSONA.md lives in the workspace ROOT — control-plane, never under mnt/ (ADR-10) — so
// the model can neither read nor rewrite its own identity; a self-writable persona would
// be a prompt-injection vector onto the assistant itself.
func loadPersona(dir string) string {
	for _, p := range []string{
		filepath.Join(dir, "PERSONA.md"),
		filepath.Join(filepath.Dir(dir), "PERSONA.md"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s
			}
		}
	}
	return defaultPersona
}

// toolTimeout bounds a single tool call inside the loop.
const toolTimeout = 20 * time.Second

// scriptTimeout bounds a whole code.run evaluation.
const scriptTimeout = 60 * time.Second

// httpTimeout bounds a single outbound HTTP effect.
const httpTimeout = 15 * time.Second

// approvalTTL is how long an out-of-band approval stays answerable.
const approvalTTL = 2 * time.Minute

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

	guard := &gateway.Guard{Policy: base(), Approvals: h.Approvals, TTL: approvalTTL}

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
		persona:     loadPersona(dir),
	}, nil
}

// OpenSession opens an interactive session over this workspace's parts — concrete
// references, no service-locator indirection. Its "Allow always" grants persist to the
// workspace's own grants store; "Allow this session" grants die on Reset/Close.
func (w *Workspace) OpenSession() *session.Session {
	return session.New(w.loop, w.tools, w.guard, w.grants, session.WithPersona(w.persona))
}

// RunAgent runs the named child agent to completion over this workspace's tools + guard,
// with the agent's OWN durable grants (per-agent file). An unknown name returns an error.
func (w *Workspace) RunAgent(ctx context.Context, name, task string) (agent.Result, error) {
	var def agent.Agent
	for _, a := range w.agents {
		if a.Name == name {
			def = a
			break
		}
	}
	return agent.Run(ctx, w.deps(), def, task)
}

// deps bundles this workspace's parts for a child-agent run: the shared loop + tools +
// guard, and a resolver for each agent's own durable "always" store.
func (w *Workspace) deps() agent.Deps {
	agentsDir := filepath.Join(w.dir, "agents")
	return agent.Deps{
		Brain: w.loop,
		Tools: w.tools,
		Guard: w.guard,
		Store: func(name string) capability.GrantStore { return grantstore.Load(grantstore.Path(agentsDir, name)) },
	}
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
