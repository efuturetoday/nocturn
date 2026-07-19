// Package gateway is the authorization use-case: the one reusable pipeline that
// decides whether a real-world effect may proceed.
//
// Its core is the Guard: the broker decision (allow / ask / deny, with epoch,
// time window, and rate limit) plus out-of-band human approval on Ask, wrapped by
// Do (authorize-then-execute). The concrete effects live OUTSIDE this package, in
// interface-adapter packages (e.g. netcap for http/dns) that each hold a *Guard
// and run every call through it before doing any I/O. Adding a capability is a new
// small adapter type — never a new field on a god-object here. This package
// therefore depends only inward (capability + hitl), not on any effect adapter.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// ErrDenied is returned when a capability call is not permitted (broker Deny, or
// a human denied / did not approve in time).
var ErrDenied = errors.New("gateway: capability denied")

// RateLimitedError is returned when a call is refused because its family is over its
// rate budget. It carries RetryAfter — how long until a slot frees — so the caller
// (ultimately the model, via the tool result) can wait, schedule a wake, or tell the
// user it cannot act right now. It unwraps to ErrDenied, so existing errors.Is(err,
// ErrDenied) checks still hold; the effect did not happen.
type RateLimitedError struct {
	Family     string
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limit reached for %q: retry in ~%s (wait that long, or schedule a wake, or tell the user it can't be done right now)", e.Family, e.RetryAfter.Round(time.Second))
}

// Unwrap makes errors.Is(err, ErrDenied) hold — a rate refusal IS a denial, just an
// informative one.
func (e *RateLimitedError) Unwrap() error { return ErrDenied }

// rateCheck consults the guard's rate limiter for call's family, recording the call when
// allowed. It returns a *RateLimitedError (with the retry-after) when over budget, or nil
// when allowed or when no limiter is configured / the family is unlimited.
func (g *Guard) rateCheck(call capability.Call) error {
	if g.Rate == nil {
		return nil
	}
	if ok, retry := g.Rate.Allow(call.Family); !ok {
		return &RateLimitedError{Family: call.Family, RetryAfter: retry}
	}
	return nil
}

// ErrScanUnspecified is returned when Do is called with a zero-value EgressScan —
// i.e. neither ScanEgress(...) nor WithoutScan() was chosen. Every effect MUST
// declare its egress-scan decision; failing to is a fail-closed programming error.
var ErrScanUnspecified = errors.New("gateway: egress scan decision not specified")

// ErrEmptyEgress is returned when an effect declared ScanEgress (external) but its
// egress surface came out empty — almost always a bug: the caller forgot to extract
// the outbound bytes. A genuinely external effect always has a target/URL/name to
// scan, so an empty surface fails closed instead of scanning nothing.
var ErrEmptyEgress = errors.New("gateway: external effect declared no egress surface to scan")

// Guard authorizes capability calls. It is host-trusted and shared by every
// capability group. It is a pure COMPOSER: the standing-grant state lives on the
// active capability.Grants (not on the Guard), and upper bounds live in the
// cage chain carried by ctx — so the Guard holds no per-session mutable state.
type Guard struct {
	Policy capability.Policy
	// Approvals is the human-approval engine. WHICH channel a given request uses —
	// interactive for an attended run, out-of-band (phone) for an unattended one — is
	// the engine's concern (see hitl.WithRouter), not the Guard's: the Guard decides
	// the verdict ("ask a human"), the engine decides how and where to reach them.
	Approvals *hitl.Engine
	Rate      *capability.RateLimiter
	TTL       time.Duration
	Now       func() time.Time

	// Log records each Authorize verdict (family/write/target → allow|deny) — the core diagnostic
	// + audit line. It logs identifiers, never secret values. A nil Log defaults to a no-op via
	// logger(), so a zero-value Guard needs no special-casing at the call site.
	Log *slog.Logger

	// epochs is the guard's OWN revocation registry — the single source of truth for
	// which permission scopes are alive. It is unexported so no caller can hand the
	// Guard a different registry than the one its Scopes are minted on: the old
	// "must be the guard's registry so grants and revocation line up" comment-invariant
	// is now a type-invariant (a Scope can only come from Guard.NewScope). It is
	// lazily created (once) so a struct-literal Guard with no Scope stays as before
	// (an empty registry behaves identically to nil for the base policy's Permanent
	// rules — only epoch-bound session grants consult it, and those only exist once a
	// Scope has been opened).
	epochsOnce sync.Once
	epochs     *capability.EpochRegistry
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// discardLog is the shared no-op logger a Guard falls back to when Log is unset.
var discardLog = slog.New(slog.DiscardHandler)

// logger returns g.Log or the shared no-op — so every log site calls it unconditionally, with no
// scattered nil checks.
func (g *Guard) logger() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return discardLog
}

// epochRegistry returns the guard's revocation registry, creating it once on first
// use. Both NewScope (mint) and Authorize (liveness check) go through it, so the
// write is published under the same sync.Once that every reader observes — no data
// race between opening a scope and authorizing a concurrent call.
func (g *Guard) epochRegistry() *capability.EpochRegistry {
	g.epochsOnce.Do(func() { g.epochs = capability.NewEpochRegistry() })
	return g.epochs
}

// approvalChoicesFor builds the options a human is offered on Ask, labelling the
// session/always choices with EXACTLY what would be remembered — the model-facing
// tool (and, for "always", the target). The label must name the grant's real
// scope: a human who reads "Send email to Bob" must see that "always" remembers
// "gmail.send @ gmail.googleapis.com" (any recipient), never a bare "Allow always"
// that hides a broad standing grant behind a narrow prompt.
func approvalChoicesFor(toolName string, call capability.Call) []hitl.Choice {
	name := toolName
	if name == "" {
		name = call.Family // native/direct call: the primitive family is the tool
	}
	always := "Allow always: " + name
	if call.Target != "" {
		always += " @ " + call.Target
	}
	return []hitl.Choice{
		{Label: "Allow once", Outcome: hitl.Approved},
		{Label: "Allow this session: " + name, Outcome: hitl.ApprovedSession},
		{Label: always, Outcome: hitl.ApprovedAlways},
		{Label: "Deny", Outcome: hitl.Denied},
	}
}

// consequentialChoices are the only options for a never-auto (consequential) effect:
// once or deny — never a standing grant.
var consequentialChoices = []hitl.Choice{
	{Label: "Allow once", Outcome: hitl.Approved},
	{Label: "Deny", Outcome: hitl.Denied},
}

// factLine is the unforgeable, host-computed line shown beneath a semantic intent
// at every Ask: the model-facing tool and the reconstructed (capability, target)
// it actually reaches. It comes only from the Call the host built — never from
// guest or model text — so a semantic template can NEVER hide what is really
// gated. Empty target (a targetless call like "log") omits the "@ target".
func factLine(toolName string, call capability.Call) string {
	name := toolName
	if name == "" {
		name = call.Family
	}
	op := call.Family + " " + opWord(call.Write)
	if call.Target != "" {
		return "via " + name + " → " + op + " @ " + call.Target
	}
	return "via " + name + " → " + op
}

// opWord renders the mutation axis for a human: reads vs writes.
func opWord(write bool) string {
	if write {
		return "write"
	}
	return "read"
}

// Authorize composes the decision:
//  1. Cage chain (ctx): outside the intersection of all in-scope upper bounds
//     → hard deny, never even asking — so a prompt-injected caller can't get you
//     to approve something it was never allowed to attempt.
//  2. Base policy: Allow → proceed; Deny → deny (deny-wins hard rail).
//  3. On Ask: a standing grant in the active grant set (session or always) short-
//     circuits; otherwise out-of-band human approval, and the chosen scope
//     (once/session/always) is recorded as a grant on the grant set.
//  4. On Ask with no standing grant, the autonomy dial (capability.AutonomyFrom)
//     resolves an UNATTENDED run: strict → deny, full → auto-allow (non-consequential),
//     attended/guarded → ask out of band. Inert for a normal (attended) run.
func (g *Guard) Authorize(ctx context.Context, call capability.Call, intent string) (err error) {
	// One line per decision, capturing EVERY return path (allow / ask→outcome / deny / rate).
	// Identifiers only (family/write/target), never the intent text or any value.
	defer func() {
		outcome := "allow"
		if err != nil {
			outcome = "deny"
		}
		g.logger().InfoContext(ctx, "authorize",
			slog.String("family", call.Family),
			slog.Bool("write", call.Write),
			slog.String("target", call.Target),
			slog.String("outcome", outcome),
			slog.Any("err", err))
	}()

	if !capability.WithinCages(ctx, call) {
		return ErrDenied
	}
	env := capability.Env{Now: g.now(), Epochs: g.epochRegistry()}

	// The never-auto floor: a consequential (irreversible/high-stakes) effect ALWAYS
	// asks out of band — it can never be auto-allowed by policy, covered by a standing
	// grant, or granted for a session/always. Stamped by a trusted layer (the plugin
	// Host, from an install-reviewed ToolDecl), never by guest code.
	consequential := consequentialFrom(ctx)

	// Compose the verdict from the workspace base policy UNIONed with any scope
	// (agent) policy carried on ctx — deny>ask>allow, so a scope can tighten (deny
	// blacklist, or ask where the base allows). Loosening is the deferred autonomy
	// dial (see capability.WithPolicy).
	policy := g.Policy
	if extra := capability.PolicyRulesFrom(ctx); len(extra) > 0 {
		rules := append([]capability.Rule(nil), g.Policy.Rules...)
		rules = append(rules, extra...)
		policy = capability.Policy{Rules: rules}
	}
	decision := policy.Evaluate(call, env)
	if consequential && decision == capability.Allow {
		decision = capability.Ask // escalate: consequential is never auto
	}

	switch decision {
	case capability.Allow:
		// The rate cap applies even to a base-policy Allow, so a silent, always-allowed
		// effect (notify) still has anti-spam. Reads are unconfigured in the limiter →
		// unlimited (see capability.RateLimiter), so bursty file/http reads pass freely.
		return g.rateCheck(call)
	case capability.Ask:
		grants := capability.GrantsFrom(ctx)
		toolName := tool.Name(ctx) // the model-facing tool a grant is remembered against
		if !consequential && grants != nil && grants.Allows(toolName, call, env) {
			// A standing grant answers the Ask — but still respects the rate cap, so a
			// remembered "always" cannot be replayed without bound (the grant path used
			// to bypass the limiter).
			if err := g.rateCheck(call); err != nil {
				return err
			}
			return nil // standing grant (session or always), scoped to this tool
		}
		// Autonomy dial: an UNATTENDED run (scheduled/webhook, no human at the console)
		// has no one to answer a live prompt, so the Ask is resolved by the run's
		// autonomy level instead. A standing grant (checked just above) already answered
		// where one exists; the dial only governs an otherwise-live Ask. It never loosens
		// the cage or a deny (those ran earlier and win), and a consequential effect is
		// never auto-allowed (the floor wins). AutonomyAttended/Guarded fall through to
		// the out-of-band request — Guarded's channel still reaches a human on their phone.
		switch capability.AutonomyFrom(ctx) {
		case capability.AutonomyStrict:
			return ErrDenied // unattended + strict: never act without a human
		case capability.AutonomyFull:
			if !consequential {
				if err := g.rateCheck(call); err != nil {
					return err
				}
				return nil // unattended + full: auto-allow within the cage + policy
			}
		}
		// A higher, trusted layer (a plugin Host rendering an install-reviewed
		// manifest template) may have stamped a semantic intent onto ctx — show it
		// as the human-readable HEAD so the human reads "Send email to x@a". But the
		// host-computed fact line ALWAYS rides beneath it (never replaced), so a
		// semantic template can't hide the real (capability, target) being gated.
		prompt := intent
		if r := intentFrom(ctx); r != "" {
			prompt = r + "\n" + factLine(toolName, call)
		}
		// A source label (e.g. the workspace of a background/unattended run) prefixes the
		// prompt so a human answering out of band knows WHICH context is asking — "[work]
		// Send email …" rather than a context-free prompt on the phone.
		if lbl := labelFrom(ctx); lbl != "" {
			prompt = "[" + lbl + "] " + prompt
		}
		// A consequential effect offers only once-or-deny: no "session"/"always", so it
		// can never become a standing grant.
		choices := approvalChoicesFor(toolName, call)
		if consequential {
			choices = consequentialChoices
		}
		out, err := g.Approvals.Request(ctx, prompt, choices, g.TTL)
		if err != nil {
			return err
		}
		switch out {
		case hitl.ApprovedAlways:
			if grants != nil && !consequential {
				_ = grants.Record(toolName, call, capability.ScopeAlways) // persist error must not block the allow
			}
			return nil
		case hitl.ApprovedSession:
			if grants != nil && !consequential {
				_ = grants.Record(toolName, call, capability.ScopeSession)
			}
			return nil
		case hitl.Approved:
			return nil
		default:
			return ErrDenied
		}
	default:
		return ErrDenied
	}
}

// EgressScanner is the narrow behaviour Do needs to leak-scan an outbound surface.
// *secret.Scanner satisfies it structurally — so gateway never imports secret and
// stays decoupled from the credential layer (accept a narrow interface, don't
// depend on the concrete type).
type EgressScanner interface {
	// ScanEgress reports a leak in any outbound part; nil means clean.
	ScanEgress(parts ...string) error
}

// EgressScan is the MANDATORY leak-scan decision every Do carries. Because it is a
// required argument, a new (external) capability cannot silently ship unscanned —
// the author must choose at compile time between ScanEgress (an external effect,
// whose outbound surface is scanned) and WithoutScan (a local effect — file/compute
// — that never crosses to untrusted infra, an explicit and grep-able opt out). The
// zero value is invalid and fails closed (ErrScanUnspecified).
type EgressScan struct {
	sc     EgressScanner
	egress func() []string // the outbound surface — lazy, only built once authorized
	omit   bool            // WithoutScan
}

// ScanEgress marks an external effect: its outbound surface (egress) is leak-scanned
// with sc before the effect runs. egress is lazy so it is only assembled once the
// call is authorized. A declared-but-empty surface fails closed (ErrEmptyEgress).
func ScanEgress(sc EgressScanner, egress func() []string) EgressScan {
	return EgressScan{sc: sc, egress: egress}
}

// WithoutScan is the explicit opt out for a LOCAL effect that never reaches
// untrusted external infra (file.*, compute). It is deliberately visible so every
// unscanned effect is auditable — `grep WithoutScan` lists them all.
func WithoutScan() EgressScan { return EgressScan{omit: true} }

// Do authorizes call (out-of-band HITL on Ask), then — for an external effect —
// leak-scans the outbound surface, and runs effect ONLY if the call is allowed AND
// the scan is clean. Because the effect is a closure, it is unreachable unless
// Authorize returned nil and the egress scan passed: a capability method physically
// cannot run its effect without gating and (when external) scanning first — bypass
// is impossible by construction. The scan runs BEFORE the effect (hence before any
// credential the effect injects, so the host's own bearer is never flagged). A free
// function, not a method, so it can be generic over the effect's result type.
func Do[T any](ctx context.Context, g *Guard, call capability.Call, intent string, es EgressScan, effect func() (T, error)) (T, error) {
	var zero T
	if !es.omit && (es.sc == nil || es.egress == nil) {
		return zero, ErrScanUnspecified // neither ScanEgress(...) nor WithoutScan() — fail closed
	}
	if err := g.Authorize(ctx, call, intent); err != nil {
		return zero, err
	}
	if !es.omit {
		parts := es.egress()
		if !anyNonEmpty(parts) {
			return zero, ErrEmptyEgress // external declared, nothing extracted → caller bug
		}
		if err := es.sc.ScanEgress(parts...); err != nil {
			return zero, err // ErrLeaked → the effect never runs
		}
	}
	return effect()
}

// anyNonEmpty reports whether ss holds at least one non-blank string.
func anyNonEmpty(ss []string) bool {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

// intentKey carries a higher layer's semantic intent for the current operation.
type intentKey struct{}

// WithIntent attaches a human-readable intent that Authorize prefers over the
// effect tool's own (transport-level) intent when prompting. It is set by a
// TRUSTED layer only — the plugin Host, rendering a template that was reviewed at
// install time — never by guest code, so a plugin cannot forge a misleading
// prompt for an effect it isn't allowed to attempt (the cage still bounds the
// real target, and that is what is gated).
func WithIntent(ctx context.Context, intent string) context.Context {
	return context.WithValue(ctx, intentKey{}, intent)
}

func intentFrom(ctx context.Context) string {
	s, _ := ctx.Value(intentKey{}).(string)
	return s
}

// labelKey carries a source label for the current operation — e.g. the workspace of a
// background/unattended run — prefixed onto the HITL prompt so a human answering out
// of band knows which context is asking. Purely presentational (never gates anything).
type labelKey struct{}

// WithLabel attaches a source label (e.g. a workspace name) shown as a prefix on the
// approval prompt. Set by a trusted layer (the scheduler/composition root), not guest code.
func WithLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, labelKey{}, label)
}

func labelFrom(ctx context.Context) string {
	s, _ := ctx.Value(labelKey{}).(string)
	return s
}

// consequentialKey marks the current operation as never-auto (see ToolDecl.Consequential).
type consequentialKey struct{}

// WithConsequential marks the current operation as consequential (irreversible/
// high-stakes): Authorize will always ask out of band and never let a standing
// grant answer or be recorded. Set by a TRUSTED layer only (the plugin Host, from
// an install-reviewed manifest flag), never by guest code — so a plugin cannot mark
// its own effect as NON-consequential to dodge the floor (absence is the default,
// and the flag only ever tightens).
func WithConsequential(ctx context.Context) context.Context {
	return context.WithValue(ctx, consequentialKey{}, true)
}

func consequentialFrom(ctx context.Context) bool {
	v, _ := ctx.Value(consequentialKey{}).(bool)
	return v
}
