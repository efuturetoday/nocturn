// Package skill is the skills layer: procedural knowledge the model loads on
// demand (the agentskills.io open standard). A skill is a directory with a
// SKILL.md (YAML frontmatter + Markdown body); the frontmatter's name +
// description form a cheap catalog, the body loads only when a skill is
// activated (progressive disclosure). Skills are CONTEXT, never tools — they
// shape HOW the model uses its real, gated tools; they carry zero authority, so
// they need no sandbox and cannot widen what the broker permits.
//
// This package is host-side glue: it reads and parses skill files and depends on
// nothing but the stdlib and yaml.v3. Discovery is deliberately read-only; the
// activation tools that surface skills to the model live in a later layer.
package skill

import (
	"errors"
	"os"
	"path/filepath"
)

// maxBodyBytes caps a single SKILL.md read — a fail-safe against a pathological
// file; real skill bodies are recommended under ~5000 tokens (spec).
const maxBodyBytes = 256 << 10

// SkillFile is the required filename inside a skill directory.
const SkillFile = "SKILL.md"

// Skill is one discovered skill: the parsed frontmatter plus where it lives. The
// body is NOT held in memory — it is read on demand (progressive disclosure) via
// Body, so the catalog stays cheap.
type Skill struct {
	Name          string
	Description   string
	License       string
	Compatibility string
	Metadata      map[string]string

	// AllowedTools is the spec's experimental pre-approval field. It is parsed for
	// fidelity but DELIBERATELY UNUSED: in Nocturn the broker + HITL are the sole
	// authority, so a text file must never be able to widen what a tool may do.
	AllowedTools string

	Dir      string // absolute skill directory
	Location string // scope label, e.g. "workspace", "user"
}

// Body reads the Markdown body of the skill's SKILL.md (everything after the
// frontmatter), capped at maxBodyBytes. Read on demand so the body only enters
// context when the skill is actually activated.
func (s Skill) Body() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, SkillFile))
	if err != nil {
		return "", err
	}
	if len(data) > maxBodyBytes {
		data = data[:maxBodyBytes]
	}
	_, body, err := parseFrontmatter(data)
	return body, err
}

// Diagnostic records a skill that was skipped or loaded with a warning, so the
// operator can see why a skill did not appear (surfaced later via /skills).
type Diagnostic struct {
	Dir     string
	Level   Level
	Message string
}

// Level is the severity of a Diagnostic.
type Level int

const (
	// Warn: the skill loaded despite a lenient-fixable issue (e.g. name≠dir).
	Warn Level = iota
	// Skip: the skill was rejected (e.g. missing description, unparseable).
	Skip
)

func (l Level) String() string {
	if l == Warn {
		return "warn"
	}
	return "skip"
}

// Index is the set of discovered skills, ordered by discovery, keyed by name,
// with the diagnostics collected along the way.
type Index struct {
	byName map[string]Skill
	order  []string
	Diags  []Diagnostic
}

func newIndex() *Index { return &Index{byName: map[string]Skill{}} }

// Get returns the skill with the given name.
func (ix *Index) Get(name string) (Skill, bool) {
	s, ok := ix.byName[name]
	return s, ok
}

// Skills returns the discovered skills in discovery order.
func (ix *Index) Skills() []Skill {
	out := make([]Skill, 0, len(ix.order))
	for _, n := range ix.order {
		out = append(out, ix.byName[n])
	}
	return out
}

// Len reports how many skills were discovered.
func (ix *Index) Len() int { return len(ix.order) }

// add inserts a skill unless its name already exists (first scope wins); a
// collision is recorded as a Warn diagnostic (shadowing).
func (ix *Index) add(s Skill) {
	if _, exists := ix.byName[s.Name]; exists {
		ix.Diags = append(ix.Diags, Diagnostic{
			Dir: s.Dir, Level: Warn,
			Message: "skill " + s.Name + " shadowed: a skill with this name was already found in a higher-precedence scope",
		})
		return
	}
	ix.byName[s.Name] = s
	ix.order = append(ix.order, s.Name)
}

func (ix *Index) diag(dir string, level Level, msg string) {
	ix.Diags = append(ix.Diags, Diagnostic{Dir: dir, Level: level, Message: msg})
}

// errNoFrontmatter is returned when a SKILL.md lacks a --- delimited frontmatter.
var errNoFrontmatter = errors.New("skill: no frontmatter (expected a leading --- block)")
