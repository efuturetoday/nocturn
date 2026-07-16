package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"gopkg.in/yaml.v3"
)

// Definition is a workspace agent: WHAT it does (Instructions = the markdown
// body), WHICH tools it may use (Tools, by group name "gmail" or exact name
// "github.search"), and WHEN it runs (When). It is loaded from
// <ws>/.agents/<name>.md — control-plane (outside the model's mount, ADR-10), so
// the model cannot author its own agents. Tools are the ONLY authority surface;
// skills (context, zero authority) are opt-in via the tools list (add "skill") —
// off by default so a focused agent carries no skill-catalog context.
type Definition struct {
	Name         string
	Description  string        // one-line summary (shown when listing/selecting agents)
	Instructions string        // the markdown body — the agent's task framing
	Model        string        // optional model override; "" = default
	Tools        []string      // group ("gmail") or exact ("github.search") tool names
	When         string        // "manual" | a cron expr | "webhook" (v1: manual is what runs)
	Budget       time.Duration // wall-clock budget per run; 0 = caller default
	// Policy is the agent author's OWN standing policy, composed onto the workspace
	// base for this agent's runs (deny>ask>allow union — see capability.WithPolicy).
	// It can TIGHTEN: Deny (a blacklist, deny-wins) or Ask where the base allows.
	// Loosening (Allow over a base Ask) is the deferred autonomy dial. This is author
	// config — NEVER a grant (grants are runtime HITL consent, see KONZEPT §9).
	Policy capability.Policy
	// Cage is an optional per-agent reachability upper bound, intersected with any
	// outer cage — e.g. confine a raw-http agent to one host regardless of its tools.
	Cage []capability.Pair
}

// Matches reports whether a registry tool name is one this agent may use. A list
// entry is an exact tool name ("github.search") or a GROUP: "gmail", "gmail.*",
// or "gmail/*" (the VS Code style) all match every "gmail.*" tool.
func (d Definition) Matches(toolName string) bool {
	for _, t := range d.Tools {
		group := strings.TrimSuffix(strings.TrimSuffix(t, "/*"), ".*")
		if toolName == t || toolName == group || strings.HasPrefix(toolName, group+".") {
			return true
		}
	}
	return false
}

type frontmatter struct {
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Model       string           `yaml:"model"`
	Tools       []string         `yaml:"tools"`
	When        string           `yaml:"when"`
	Budget      string           `yaml:"budget"` // Go duration, e.g. "5m"; "" = default
	Policy      []policyRuleFM   `yaml:"policy"`
	Cage     []cageEntryFM `yaml:"cage"`
}

// policyRuleFM is one author-declared policy rule. Access ∈ read|write|any (default
// any); Effect ∈ deny|ask (allow/loosening is the deferred autonomy dial).
type policyRuleFM struct {
	Effect string `yaml:"effect"`
	Family string `yaml:"family"`
	Target string `yaml:"target"`
	Access string `yaml:"access"`
}

// cageEntryFM is one reachability upper-bound entry (like a plugin Require:
// mutates=true grants read+write, write⊇read; false is read-only).
type cageEntryFM struct {
	Family  string `yaml:"family"`
	Target  string `yaml:"target"`
	Mutates bool   `yaml:"mutates"`
}

// accessMatch maps an author's access string to the mutation-match class.
func accessMatch(access string) (capability.Match, error) {
	switch access {
	case "", "any":
		return capability.MatchAny, nil
	case "read":
		return capability.MatchRead, nil
	case "write":
		return capability.MatchWrite, nil
	default:
		return capability.MatchNone, fmt.Errorf("access must be read, write or any (got %q)", access)
	}
}

// buildPolicy turns author policy rules into a capability.Policy. Only deny/ask are
// allowed (tightening); allow (loosening) is rejected with a clear message — it needs
// the autonomy dial's precedence layer (Phase 4), not a silent no-op.
func buildPolicy(rules []policyRuleFM) (capability.Policy, error) {
	out := make([]capability.Rule, 0, len(rules))
	for _, r := range rules {
		if r.Family == "" || r.Target == "" {
			return capability.Policy{}, fmt.Errorf("policy rule needs family and target (use \"*\" for any)")
		}
		var eff capability.Decision
		switch r.Effect {
		case "deny":
			eff = capability.Deny
		case "ask":
			eff = capability.Ask
		case "allow":
			return capability.Policy{}, fmt.Errorf("policy effect \"allow\" (loosening) is not supported yet — that is the autonomy dial; use deny or ask")
		default:
			return capability.Policy{}, fmt.Errorf("policy effect must be deny or ask (got %q)", r.Effect)
		}
		writes, err := accessMatch(r.Access)
		if err != nil {
			return capability.Policy{}, err
		}
		out = append(out, capability.Rule{Family: r.Family, TargetGlob: r.Target, Writes: writes, Effect: eff, Epoch: capability.Permanent})
	}
	return capability.Policy{Rules: out}, nil
}

// buildCage turns author cage entries into capability.Pairs.
func buildCage(entries []cageEntryFM) ([]capability.Pair, error) {
	pairs := make([]capability.Pair, 0, len(entries))
	for _, e := range entries {
		if e.Family == "" || e.Target == "" {
			return nil, fmt.Errorf("cage entry needs family and target")
		}
		writes := capability.MatchRead
		if e.Mutates {
			writes = capability.MatchAny
		}
		pairs = append(pairs, capability.Pair{Family: e.Family, TargetGlob: e.Target, Writes: writes})
	}
	return pairs, nil
}

const maxAgentBytes = 64 << 10

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// An agent is a self-contained FOLDER: <dir>/<name>/agent.md (definition) plus,
// once granted, <dir>/<name>/grants.json (this agent's own "always" grants). The
// folder is the portable/purgeable unit (ADR-10, KONZEPT §9): deleting it removes
// the agent AND its grants; its grants can never cross-match another owner's.
const (
	agentFile  = "agent.md"
	grantsFile = "grants.json"
)

// GrantsPath returns the per-agent grants file inside its folder.
func GrantsPath(agentsDir, name string) string {
	return filepath.Join(agentsDir, name, grantsFile)
}

// LoadAgents reads every <dir>/<name>/agent.md agent definition. A missing dir
// yields no agents (nil, nil). A subfolder without agent.md is skipped (it may hold
// only grants). A malformed definition is an error naming the file, fail-closed —
// the operator never gets a half-understood agent. Returned sorted by name.
func LoadAgents(dir string) ([]Definition, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent: read %s: %w", dir, err)
	}
	var defs []Definition
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue // each agent is a folder
		}
		md := filepath.Join(dir, e.Name(), agentFile)
		if info, err := os.Stat(md); err != nil || info.IsDir() {
			continue // folder without an agent.md — not an agent
		}
		def, err := loadAgent(md, e.Name())
		if err != nil {
			return nil, err
		}
		if seen[def.Name] {
			return nil, fmt.Errorf("agent: duplicate name %q", def.Name)
		}
		seen[def.Name] = true
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

func loadAgent(path, defaultName string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, fmt.Errorf("agent: read %s: %w", path, err)
	}
	if len(data) > maxAgentBytes {
		return Definition{}, fmt.Errorf("agent %s: file exceeds %d bytes", path, maxAgentBytes)
	}
	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return Definition{}, fmt.Errorf("agent %s: %w", path, err)
	}
	var f frontmatter
	if err := yaml.Unmarshal(fm, &f); err != nil {
		return Definition{}, fmt.Errorf("agent %s: invalid frontmatter: %w", path, err)
	}

	name := f.Name
	if name == "" {
		name = defaultName // the agent's folder name
	}
	if !nameRe.MatchString(name) {
		return Definition{}, fmt.Errorf("agent %s: invalid name %q (want %s)", path, name, nameRe)
	}
	instructions := strings.TrimSpace(string(body))
	if instructions == "" {
		return Definition{}, fmt.Errorf("agent %s: empty instructions (the markdown body)", path)
	}
	if len(f.Tools) == 0 {
		return Definition{}, fmt.Errorf("agent %s: declare at least one tool", path)
	}
	var budget time.Duration
	if f.Budget != "" {
		if budget, err = time.ParseDuration(f.Budget); err != nil {
			return Definition{}, fmt.Errorf("agent %s: bad budget %q: %w", path, f.Budget, err)
		}
	}
	when := strings.TrimSpace(f.When)
	if when == "" {
		when = "manual"
	}
	policy, err := buildPolicy(f.Policy)
	if err != nil {
		return Definition{}, fmt.Errorf("agent %s: %w", path, err)
	}
	cage, err := buildCage(f.Cage)
	if err != nil {
		return Definition{}, fmt.Errorf("agent %s: %w", path, err)
	}
	return Definition{
		Name: name, Description: strings.TrimSpace(f.Description), Instructions: instructions,
		Model: f.Model, Tools: f.Tools, When: when, Budget: budget,
		Policy: policy, Cage: cage,
	}, nil
}

// splitFrontmatter separates a leading `---`-delimited YAML block from the
// markdown body. The file must open with a `---` line; the block ends at the next
// line that is exactly `---` (or `...`). Mirrors the SKILL.md convention.
func splitFrontmatter(src []byte) (fm, body []byte, err error) {
	rest, ok := bytes.CutPrefix(bytes.TrimLeft(src, "\ufeff \t\r\n"), []byte("---\n"))
	if !ok {
		return nil, nil, errors.New("missing --- frontmatter block")
	}
	start := 0
	for start <= len(rest) {
		nl := bytes.IndexByte(rest[start:], '\n')
		line := rest[start:]
		if nl >= 0 {
			line = rest[start : start+nl]
		}
		if t := bytes.TrimRight(line, "\r"); bytes.Equal(t, []byte("---")) || bytes.Equal(t, []byte("...")) {
			fm = rest[:start]
			if nl < 0 {
				return fm, nil, nil
			}
			return fm, rest[start+nl+1:], nil
		}
		if nl < 0 {
			break
		}
		start += nl + 1
	}
	return nil, nil, errors.New("unterminated frontmatter (no closing ---)")
}
