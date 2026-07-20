package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Skill-name / description limits, verified against the Anthropic Agent Skills spec. The name rule
// is STRICTER than a tool name: lowercase letters, digits and hyphens only (no underscore), and it
// may not contain the reserved words "anthropic" or "claude". Hard, enforced at construction.
const (
	MaxSkillNameLen  = 64
	MaxSkillDescLen  = 1024
	SkillNamePattern = `^[a-z0-9-]{1,64}$`

	// loadSkillToolName is the progressive-disclosure tool a SkillSet exposes. The provider tool-name
	// regex (ToolNamePattern) allows only letters, digits, '_' and '-' — no '.', so this is
	// "load_skill", not "skill.load", to pass validation.
	loadSkillToolName = "load_skill"
)

var skillNameRe = regexp.MustCompile(SkillNamePattern)

// Skill is one progressive-disclosure entry: name + description (the catalog the model sees) plus
// body. Context, zero authority — the skills analog of a tool. WHERE a Skill comes from (disk,
// embed, network, the moon) is NOT this package's concern; the consumer builds Skill values and
// hands them in, exactly like tools.
type Skill struct {
	Name        string
	Description string
	Body        string
}

// Validate enforces the hard Agent-Skills rules: Name matches SkillNamePattern and contains neither
// "anthropic" nor "claude"; Description is non-empty and <= MaxSkillDescLen.
func (k Skill) Validate() error {
	if !skillNameRe.MatchString(k.Name) {
		return fmt.Errorf("agentkit: invalid skill name %q: must match %s", k.Name, SkillNamePattern)
	}
	if strings.Contains(k.Name, "anthropic") || strings.Contains(k.Name, "claude") {
		return fmt.Errorf("agentkit: skill name %q: must not contain reserved words", k.Name)
	}
	if k.Description == "" {
		return fmt.Errorf("agentkit: skill %q: description is required", k.Name)
	}
	if len(k.Description) > MaxSkillDescLen {
		return fmt.Errorf("agentkit: skill %q: description exceeds %d chars", k.Name, MaxSkillDescLen)
	}
	return nil
}

// SkillSet is the skills available to a session, keyed by name — a plain named map (like ToolSet),
// so index/range/len work; the domain methods sit on top. Select returns a COPY.
type SkillSet map[string]Skill

// NewSkillSet builds a SkillSet keyed by Name, validating each skill. It returns an error if any
// skill is invalid or two share a name — so a SkillSet never holds an invalid or colliding skill.
func NewSkillSet(skills ...Skill) (SkillSet, error) {
	set := make(SkillSet, len(skills))
	for _, k := range skills {
		if err := k.Validate(); err != nil {
			return nil, err
		}
		if _, dup := set[k.Name]; dup {
			return nil, fmt.Errorf("agentkit: duplicate skill name %q", k.Name)
		}
		set[k.Name] = k
	}
	return set, nil
}

// Select returns a NEW SkillSet holding only skills whose name satisfies keep.
func (s SkillSet) Select(keep func(name string) bool) SkillSet {
	out := make(SkillSet)
	for name, k := range s {
		if keep(name) {
			out[name] = k
		}
	}
	return out
}

// Get returns a skill by name.
func (s SkillSet) Get(name string) (Skill, bool) {
	k, ok := s[name]
	return k, ok
}

// Specs returns the catalog (name + description, bodies omitted) the model sees for progressive
// disclosure, sorted by name.
func (s SkillSet) Specs() []Skill {
	out := make([]Skill, 0, len(s))
	for _, k := range s {
		out = append(out, Skill{Name: k.Name, Description: k.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LoadTool returns the load_skill tool: the model calls it with a skill name to pull that skill's
// body into the conversation (progressive disclosure), backed by THIS set. Like any tool it is just
// a Tool value — no authority, only context.
func (s SkillSet) LoadTool() Tool {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"the skill to load"}},"required":["name"]}`)
	return funcTool{
		spec: ToolSpec{
			Name:        loadSkillToolName,
			Description: "Load a skill's full instructions into context by name.",
			Parameters:  schema,
		},
		fn: func(_ context.Context, args string) (string, error) {
			var in struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal([]byte(args), &in); err != nil {
				return "", fmt.Errorf("%s: invalid arguments: %w", loadSkillToolName, err)
			}
			k, ok := s[in.Name]
			if !ok {
				return "", fmt.Errorf("%s: unknown skill %q", loadSkillToolName, in.Name)
			}
			return k.Body, nil
		},
	}
}
