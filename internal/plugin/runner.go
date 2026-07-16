package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/sandbox"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// defaultTimeout bounds one plugin tool-call (a single sandbox.Run).
const defaultTimeout = 60 * time.Second

// Plugin is an installed, sandboxed plugin wired to the shared registry. Its tools
// are namespaced <name>.<tool>. Execution is STATELESS — a fresh sandbox instance
// per tool-call; cross-call state is host-mediated via the workspace filesystem
// (WorkDir, mounted at /work). Every effect reaches the broker through the one
// gate, with the plugin's ceiling stamped onto ctx so out-of-ceiling attempts are
// hard-denied.
type Plugin struct {
	Manifest Manifest
	artifact []byte
	kind     Kind
	reg      *tool.Registry
	ceiling  capability.Ceiling

	Timeout  time.Duration
	MaxPages uint32
	WorkDir  string // /work mount for cross-call state (may be "")
}

// New builds a Plugin from a Loaded package over the shared dispatch registry.
func New(l Loaded, reg *tool.Registry) *Plugin {
	return &Plugin{
		Manifest: l.Manifest,
		artifact: l.Artifact,
		kind:     l.Kind,
		reg:      reg,
		ceiling:  l.Manifest.Ceiling(),
		Timeout:  defaultTimeout,
	}
}

// Tools returns the plugin's model-facing tools, namespaced <plugin>.<tool>. Each
// tool's Invoke runs the plugin artifact for that tool.
func (p *Plugin) Tools() []tool.Tool {
	tools := make([]tool.Tool, 0, len(p.Manifest.Tools))
	for _, td := range p.Manifest.Tools {
		td := td
		tools = append(tools, tool.Tool{
			Spec: tool.Spec{
				Name:        p.Manifest.Name + "." + td.Name,
				Description: td.Description,
				Parameters:  td.Parameters,
			},
			Invoke: func(ctx context.Context, args string) (string, error) {
				// Stamp the tool's semantic intent (from the install-reviewed
				// manifest template) so a gated effect underneath prompts at the
				// human level ("Send email to x@a"), not the transport level. Only
				// the wording changes; the ceiling still bounds the real target.
				if intent := renderIntent(td.Intent, args); intent != "" {
					ctx = gateway.WithIntent(ctx, intent)
				}
				// A consequential tool (install-reviewed) hits the never-auto floor:
				// every effect it performs always asks out of band, ungrantable.
				if td.Consequential {
					ctx = gateway.WithConsequential(ctx)
				}
				return p.invoke(ctx, td.Name, args)
			},
		})
	}
	return tools
}

func (p *Plugin) invoke(ctx context.Context, toolName, args string) (string, error) {
	if p.kind == KindWASM {
		return p.runWASM(ctx, toolName, args)
	}
	return p.runJS(ctx, toolName, args)
}

// runWASM feeds {"tool":..,"args":..} on stdin to the plugin's own wasm guest.
func (p *Plugin) runWASM(ctx context.Context, toolName, args string) (string, error) {
	stdin, _ := json.Marshal(struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}{Tool: toolName, Args: rawArgs(args)})
	return p.runGuest(ctx, p.artifact, stdin)
}

// runJS runs the plugin's JS on the shared embedded QuickJS interpreter: stdin is
// the plugin source followed by a bootstrap that dispatches the named tool and
// prints its result. tool + args are embedded as sanitized JSON literals.
func (p *Plugin) runJS(ctx context.Context, toolName, args string) (string, error) {
	src := string(p.artifact) + jsBootstrap(toolName, args)
	return p.runGuest(ctx, script.InterpreterGuest(), []byte(src))
}

func jsBootstrap(toolName, args string) string {
	name, _ := json.Marshal(toolName) // safe string literal
	argsLit := string(rawArgs(args))  // sanitized JSON value literal
	// Top-level await so a rejection (e.g. a denied effect) fails the run instead
	// of becoming a swallowed unhandled rejection.
	return "\n;await (async () => {\n" +
		"  const __args = " + argsLit + ";\n" +
		"  const __out = await globalThis.plugin.tools[" + string(name) + "](__args);\n" +
		"  print(typeof __out === \"string\" ? __out : JSON.stringify(__out));\n" +
		"})();\n"
}

var placeholderRe = regexp.MustCompile(`\{[a-zA-Z0-9_]+\}`)

// renderIntent fills {field} placeholders in tmpl from the call's JSON args
// (e.g. "Send an email to {to}" + {"to":"x@a"} → "Send an email to x@a"). An
// unknown or unparsable field leaves its placeholder as-is; an empty template
// yields "" (the effect tool's own wording is then used).
func renderIntent(tmpl, args string) string {
	if tmpl == "" {
		return ""
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(args), &m)
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(ph string) string {
		if v, ok := m[ph[1:len(ph)-1]]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ph
	})
}

// rawArgs re-marshals args to a clean JSON literal (defends the JS bootstrap and
// the wasm payload against a malformed/injected args string); "" or invalid → {}.
func rawArgs(args string) json.RawMessage {
	var v any
	if args == "" || json.Unmarshal([]byte(args), &v) != nil {
		return json.RawMessage("{}")
	}
	b, _ := json.Marshal(v)
	return b
}

// runGuest is the shared sandbox launch: it stamps the plugin ceiling onto ctx
// (so the broker hard-denies out-of-ceiling effects), registers the one gate, and
// runs the guest to completion, returning its stdout.
//
// SECURITY: this WithCeiling call is the SOLE place a plugin's ceiling enters the
// request context, and it gates EVERY plugin effect. capability.WithinCeilings is
// deliberately fail-OPEN on an empty chain (no ceiling → vacuously allowed), so a
// plugin effect that reached the broker WITHOUT this stamp would be bounded only by
// the base policy — i.e. unbounded by the manifest. Both runJS and runWASM route
// through here precisely so that can never happen; do not add a plugin execution
// path that calls sandbox.Run without first stamping p.ceiling. The regression
// test TestPlugin_CeilingBoundsEffects_E2E locks the out-of-ceiling hard-deny.
func (p *Plugin) runGuest(ctx context.Context, guest, stdin []byte) (string, error) {
	ctx = capability.WithCeiling(ctx, p.ceiling)
	// Scope credential injection to THIS plugin: an effect from its guest only
	// picks up its own credential bindings (+ app defaults), never another
	// plugin's token, even at a shared host.
	ctx = secret.WithOwner(ctx, Owner(p.Manifest.Name))
	gate := sandbox.HostFunc{Name: "call", Fn: p.dispatch}
	res, err := sandbox.Run(ctx, guest, sandbox.Config{
		Stdin:     stdin,
		Hosts:     []sandbox.HostFunc{gate},
		Timeout:   p.Timeout,
		MaxPages:  p.MaxPages,
		Workspace: p.WorkDir,
	})
	if err != nil {
		if len(res.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, res.Stderr)
		}
		return "", err
	}
	return string(res.Stdout), nil
}

// dispatch is the one gate a plugin reaches effects through. It routes to the
// SAME shared registry the model uses — ctx still carries the plugin ceiling, so
// every effect is bounded by it. It refuses re-entry of the interpreter (code.run)
// and of the plugin's OWN tools (no self-recursion). A call to a DIFFERENT
// plugin's tool is not blocked here but is harmless: that tool's effects run under
// the intersection of both ceilings, so this plugin cannot reach a host it did not
// itself declare.
func (p *Plugin) dispatch(ctx context.Context, req []byte) ([]byte, error) {
	var c struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(req, &c); err != nil {
		return nil, fmt.Errorf("bad gate request: %w", err)
	}
	if c.Tool == "code.run" || strings.HasPrefix(c.Tool, p.Manifest.Name+".") {
		return nil, fmt.Errorf("%s is not callable from within a plugin", c.Tool)
	}
	args := string(c.Args)
	if args == "" {
		args = "{}"
	}
	out, err := p.reg.Invoke(ctx, c.Tool, args)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}
