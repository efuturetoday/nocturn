package agentkit

import "regexp"

// Skill-name / description limits, verified against the Anthropic Agent Skills spec. The name
// rule is STRICTER than a tool name: lowercase letters, digits and hyphens only (no underscore),
// and it may not contain the reserved words "anthropic" or "claude". Hard, enforced at
// construction.
const (
	MaxSkillNameLen  = 64
	MaxSkillDescLen  = 1024
	SkillNamePattern = `^[a-z0-9-]{1,64}$`
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

// Validate enforces the hard Agent-Skills rules: Name matches SkillNamePattern and contains
// neither "anthropic" nor "claude"; Description is non-empty and <= MaxSkillDescLen.
func (k Skill) Validate() error { panic("TODO") }

// SkillSet is the skills available to a session, keyed by name — a plain named map (like
// ToolSet), so index/range/len work; the domain methods sit on top. Select returns a COPY.
type SkillSet map[string]Skill

// NewSkillSet builds a SkillSet keyed by Name, validating each skill. It returns an error if any
// skill is invalid or two share a name — so a SkillSet never holds an invalid or colliding skill.
func NewSkillSet(skills ...Skill) (SkillSet, error) { panic("TODO") }

// Select returns a NEW SkillSet holding only skills whose name satisfies keep.
func (s SkillSet) Select(keep func(name string) bool) SkillSet { panic("TODO") }

// Specs returns the catalog (name + description) the model sees for progressive disclosure.
func (s SkillSet) Specs() []Skill { panic("TODO") }

// LoadTool returns the skill.load tool: the model calls it with a skill name to pull that skill's
// body into the conversation (progressive disclosure), backed by THIS set. Like any tool it is
// just a Tool value — no authority, only context.
func (s SkillSet) LoadTool() Tool { panic("TODO") }
