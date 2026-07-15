package skill

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// nameMax is the spec's frontmatter name limit; over it we warn but still load.
const nameMax = 64

// Scope is one directory to search for skills, with a label for diagnostics and
// precedence display (e.g. "workspace", "user"). Earlier scopes win on a name
// collision.
type Scope struct {
	Dir      string
	Location string
}

// Discover scans each scope for immediate subdirectories that contain a
// SKILL.md, parses their frontmatter, and returns an Index. Discovery is
// LENIENT per the agentskills.io guide: a name that mismatches its directory or
// exceeds the length limit warns but still loads; only a missing description or
// unparseable frontmatter skips the skill. Name collisions across scopes resolve
// to the earlier scope (shadowing recorded as a diagnostic). A missing scope
// directory is silently ignored (not every scope exists).
func Discover(scopes []Scope) *Index {
	ix := newIndex()
	for _, sc := range scopes {
		entries, err := os.ReadDir(sc.Dir)
		if err != nil {
			continue // absent scope → nothing to load
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue // skip files and hidden dirs (.git, …)
			}
			dir := filepath.Join(sc.Dir, e.Name())
			loadSkill(ix, dir, e.Name(), sc.Location)
		}
	}
	return ix
}

// loadSkill reads and validates one candidate skill directory, adding it to ix
// or recording a diagnostic.
func loadSkill(ix *Index, dir, dirName, location string) {
	path := filepath.Join(dir, SkillFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return // no SKILL.md → not a skill directory, silently ignore
	}
	m, _, err := parseFrontmatter(data)
	if err != nil {
		ix.diag(dir, Skip, "unparseable SKILL.md: "+err.Error())
		return
	}
	if strings.TrimSpace(m.Description) == "" {
		ix.diag(dir, Skip, "missing required frontmatter field: description")
		return
	}

	name := strings.TrimSpace(m.Name)
	switch {
	case name == "":
		name = dirName
		ix.diag(dir, Warn, "no name in frontmatter; using directory name "+dirName)
	case name != dirName:
		ix.diag(dir, Warn, "frontmatter name "+name+" does not match directory "+dirName+" (loaded anyway)")
	}
	if len(name) > nameMax {
		ix.diag(dir, Warn, "skill name exceeds "+strconv.Itoa(nameMax)+" chars (loaded anyway)")
	}

	ix.add(Skill{
		Name:          name,
		Description:   strings.TrimSpace(m.Description),
		License:       strings.TrimSpace(m.License),
		Compatibility: strings.TrimSpace(m.Compatibility),
		Metadata:      m.Metadata,
		AllowedTools:  strings.TrimSpace(m.AllowedTools),
		Dir:           dir,
		Location:      location,
	})
}
