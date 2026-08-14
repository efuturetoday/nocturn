package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/sandbox"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// defaultTimeout bounds one plugin tool-call (a single guest run).
const defaultTimeout = 60 * time.Second

// Owner is the credential-injection owner id for a plugin: "plugin:<name>". The typed prefix keeps
// plugins (and later other injecting owners, e.g. an MCP connection) in one owner namespace without
// colliding — a plugin "github" and an MCP server "github" get distinct owners.
func Owner(name string) string { return "plugin:" + name }

// SecretName is the vault key (and binding secret name) for a plugin credential, owner-namespaced so
// two plugins declaring the same credential name never share a stored value or overwrite each other's
// OAuth resolver: "plugin:<plugin>/<cred>". This mirrors mcp.SecretName; the owner prefix is the same
// boundary Owner enforces for injection, now reflected in the value's identity too.
func SecretName(pluginName, credName string) string { return Owner(pluginName) + "/" + credName }

// Plugin is a loaded, sandboxed plugin. Its tools are exposed to the model as <name>_<tool>.
// Execution is STATELESS — a fresh sandbox instance per tool-call; cross-call state is host-mediated
// via the workspace filesystem (WorkDir, mounted at /work).
//
// Its cage is a TOOLSET: `dispatch` is the base tools its guest may call, already narrowed to the
// manifest's `uses` — exactly how an agent's filter scopes a sub-agent. There is no bespoke per-plugin
// policy; a dispatched action runs under the SAME gate the model's own calls do (ctx carries the
// workspace policy + grants + approver), so a host is still approved by the human per request.
type Plugin struct {
	manifest Manifest
	artifact []byte
	kind     Kind
	dispatch agentkit.ToolSet // the base tools the guest may call, narrowed to manifest.Uses

	timeout time.Duration
	workDir string // /work mount for cross-call state (may be "")

	// A KindJS plugin runs on the process-wide shared QuickJS engine; a KindWASM plugin owns its own
	// engine, compiled lazily on first call and closed on Close. mu guards built.
	mu    sync.Mutex
	built *sandbox.Engine
}

// New builds a Plugin from a Loaded package. base is the workspace's base tools; the plugin's guest
// may call only the subset its manifest lists in `uses` — that narrowed set is its cage.
func New(l Loaded, base agentkit.ToolSet) *Plugin {
	return &Plugin{
		manifest: l.Manifest,
		artifact: l.Artifact,
		kind:     l.Kind,
		dispatch: base.Select(l.Manifest.allows),
		timeout:  defaultTimeout,
	}
}

// Name returns the plugin name.
func (p *Plugin) Name() string { return p.manifest.Name }

// Credentials returns the plugin's declared credential bindings, so the caller can register them on
// the shared injector under this plugin's owner.
func (p *Plugin) Credentials() []CredentialDecl { return p.manifest.Credentials }

// Tools returns the plugin's model-facing tools, namespaced <plugin>_<tool>. Each tool's Call runs
// the plugin artifact for that tool.
func (p *Plugin) Tools() ([]agentkit.Tool, error) {
	out := make([]agentkit.Tool, 0, len(p.manifest.Tools))
	for _, td := range p.manifest.Tools {
		schema, err := agentkit.ParseSchema(td.Parameters)
		if err != nil {
			return nil, fmt.Errorf("plugin %q tool %q: %w", p.manifest.Name, td.Name, err)
		}
		t, err := agentkit.NewTool(p.manifest.Name+"_"+td.Name, td.Description,
			func(ctx context.Context, args string) (string, error) {
				return p.run(ctx, td.Name, args)
			},
			agentkit.WithSchema(schema),
		)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Close releases a KindWASM plugin's own engine if it was built (no-op for KindJS / an uncalled
// KindWASM plugin).
func (p *Plugin) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.built == nil {
		return nil
	}
	err := p.built.Close(ctx)
	p.built = nil
	return err
}

// engine resolves the sandbox engine this plugin runs on: KindJS shares the process-wide QuickJS
// engine; KindWASM lazily compiles and caches its own guest.
func (p *Plugin) engine() (*sandbox.Engine, error) {
	if p.kind != KindWASM {
		return script.Engine()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.built == nil {
		eng, err := sandbox.New(context.Background(), p.artifact, sandbox.EngineConfig{HostNames: []string{"call"}})
		if err != nil {
			return nil, err
		}
		p.built = eng
	}
	return p.built, nil
}

// run executes one tool-call: it scopes credential injection to this plugin, then runs the guest for
// the named tool. Authority is unchanged — ctx already carries the workspace gate, and the guest can
// only dispatch to its cage (p.dispatch).
func (p *Plugin) run(ctx context.Context, toolName, args string) (string, error) {
	// Scope credential injection to THIS plugin: its guest only picks up its own bindings, never
	// another owner's token, even at a shared host.
	ctx = secret.WithOwner(ctx, Owner(p.manifest.Name))

	if p.kind == KindWASM {
		return p.runGuest(ctx, wasmStdin(toolName, args))
	}
	src := script.Prelude() + "\n" + string(p.artifact) + jsBootstrap(toolName, args)
	return p.runGuest(ctx, []byte(src))
}

// runGuest launches the guest to completion on the plugin's engine, registering the one gate, and
// returns its stdout.
func (p *Plugin) runGuest(ctx context.Context, stdin []byte) (string, error) {
	eng, err := p.engine()
	if err != nil {
		return "", err
	}
	host := sandbox.HostFunc{Name: "call", Fn: p.dispatchCall}
	res, err := eng.Run(ctx, sandbox.Config{
		Stdin:     stdin,
		Hosts:     []sandbox.HostFunc{host},
		Timeout:   p.timeout,
		Workspace: p.workDir,
	})
	if err != nil {
		if len(res.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, res.Stderr)
		}
		return "", err
	}
	return string(res.Stdout), nil
}

// dispatchCall is the one gate a plugin reaches an action through: it routes to the plugin's cage
// (p.dispatch, the base tools narrowed to its `uses`) — a tool outside the cage is simply absent and
// reports "unknown tool". It refuses re-entry of code_run and of the plugin's OWN tools.
func (p *Plugin) dispatchCall(ctx context.Context, req []byte) ([]byte, error) {
	var c struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(req, &c); err != nil {
		return nil, fmt.Errorf("bad gate request: %w", err)
	}
	if c.Tool == "code_run" || strings.HasPrefix(c.Tool, p.manifest.Name+"_") {
		return nil, fmt.Errorf("%s is not callable from within a plugin", c.Tool)
	}
	args := string(c.Args)
	if args == "" {
		args = "{}"
	}
	out, err := p.dispatch.Call(ctx, c.Tool, args)
	if err != nil {
		return nil, p.explain(err)
	}
	return []byte(out), nil
}

// explain turns "secret not found" into the one sentence that resolves it.
//
// A plugin's tools are exposed whether or not its account is connected, and that is the right way
// round: hiding them would have the assistant answer "I cannot read mail" with no hint that one
// command fixes it, and the tool set is built by a discovery pass — an authorization that happened
// afterwards would not be visible until the next one anyway. What the exposure costs is a failure at
// the boundary the first time, so that failure has to say what to do rather than name a vault key.
func (p *Plugin) explain(err error) error {
	if !errors.Is(err, secret.ErrNotFound) {
		return err
	}
	// The first declaration of each is the one to name: a plugin reaching here has exactly one
	// credential in all but contrived cases, and naming one command beats listing every possibility
	// in a message somebody reads once, in a hurry, inside a chat transcript.
	if len(p.manifest.OAuth) > 0 {
		// The PLUGIN's name, not the oauth block's: the block is called "account" or "token" because
		// it has to match a credential, and nobody installed a thing called "account".
		return fmt.Errorf("%w — the %s account is not connected yet; run: nocturn auth %s",
			err, p.manifest.Name, p.manifest.Name)
	}
	if len(p.manifest.Credentials) > 0 {
		return fmt.Errorf("%w — %s has no credential; seed it with: nocturn secret set %s",
			err, p.manifest.Name, SecretName(p.manifest.Name, p.manifest.Credentials[0].Name))
	}
	return err
}

// wasmStdin is the {"tool","args"} request fed on stdin to a wasm guest.
func wasmStdin(toolName, args string) []byte {
	b, _ := json.Marshal(struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}{Tool: toolName, Args: rawArgs(args)})
	return b
}

// jsBootstrap runs the plugin source, then dispatches the named tool and prints its result. tool +
// args are embedded as sanitized JSON literals.
func jsBootstrap(toolName, args string) string {
	name, _ := json.Marshal(toolName)
	argsLit := string(rawArgs(args))
	// Top-level await so a rejection (e.g. a denied action) fails the run instead of a swallowed
	// unhandled rejection.
	return "\n;await (async () => {\n" +
		"  const __args = " + argsLit + ";\n" +
		"  const __out = await globalThis.plugin.tools[" + string(name) + "](__args);\n" +
		"  print(typeof __out === \"string\" ? __out : JSON.stringify(__out));\n" +
		"})();\n"
}

// rawArgs re-marshals args to a clean JSON literal (defends the JS bootstrap and the wasm payload
// against a malformed/injected args string); "" or invalid → {}.
func rawArgs(args string) json.RawMessage {
	var v any
	if args == "" || json.Unmarshal([]byte(args), &v) != nil {
		return json.RawMessage("{}")
	}
	b, _ := json.Marshal(v)
	return b
}
