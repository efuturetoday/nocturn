// Package skill is nocturn's consumer-side skills layer: it loads agentskills.io skills from disk
// (a directory per skill, each with a SKILL.md of YAML frontmatter + Markdown body) into an
// agentkit.SkillSet, and provides skill_read for a skill's bundled files (Tier 3 of progressive
// disclosure). agentkit owns Tiers 1+2 (the catalog in the system prompt and skill_load); this
// package is the source adapter agentkit deliberately leaves to the consumer, plus the confined
// file reader. Skills are CONTEXT, never authority — they shape HOW the model uses its gated tools.
package skill

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/discovery"
	"github.com/efuturetoday/nocturn/internal/frontmatter"
)

const (
	// SkillFile is the required filename inside a skill directory.
	SkillFile = "SKILL.md"

	// maxBodyBytes caps a single SKILL.md read — a fail-safe against a pathological file; real skill
	// bodies are recommended under ~5000 tokens (spec).
	maxBodyBytes = 256 << 10

	// maxResourceListing bounds how many bundled files a skill advertises in its body, so a skill
	// with a huge tree does not bloat the load output.
	maxResourceListing = 40
)

// Discover scans dir (a workspace's skills/ folder) for immediate subdirectories that contain a
// SKILL.md, and returns an agentkit.SkillSet plus a name->absolute-directory map used by skill_read.
// Discovery is lenient: a skill that is unparseable, invalid against the agentkit rules (name
// pattern, missing description), or a duplicate name is SKIPPED with a diagnostic rather than failing
// the whole scan — one bad skill never blocks a workspace, and its absence is fail-closed (a skill
// carries no authority anyway). A missing skills directory yields no skills. The returned SkillSet
// only ever holds valid, deduplicated skills.
func Discover(dir string, diag *agentkit.Diagnostics) (agentkit.SkillSet, map[string]string) {
	dirs := make(map[string]string)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			discovery.Diagnose(diag, "skill", "read dir "+dir+": "+err.Error())
		}
		return agentkit.SkillSet{}, dirs
	}

	var skills []agentkit.Skill
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue // skip files and hidden dirs (.git, …)
		}
		skillDir := filepath.Join(dir, e.Name())
		sk, ok := loadOne(skillDir, e.Name(), diag)
		if !ok {
			continue
		}
		if err := sk.Validate(); err != nil {
			discovery.Diagnose(diag, "skill:"+sk.Name, "skipped (invalid): "+err.Error())
			continue
		}
		if _, dup := dirs[sk.Name]; dup {
			discovery.Diagnose(diag, "skill:"+sk.Name, "skipped (duplicate name; first wins): "+skillDir)
			continue
		}
		abs, err := filepath.Abs(skillDir)
		if err != nil {
			discovery.Diagnose(diag, "skill:"+sk.Name, "skipped (abs path): "+err.Error())
			continue
		}
		skills = append(skills, sk)
		dirs[sk.Name] = abs
	}

	// skills are already validated + deduped, so NewSkillSet cannot error — but surface it if it does.
	set, err := agentkit.NewSkillSet(skills...)
	if err != nil {
		discovery.Diagnose(diag, "skill", "build set: "+err.Error())
		return agentkit.SkillSet{}, map[string]string{}
	}
	return set, dirs
}

// loadOne reads one candidate skill directory into an agentkit.Skill. ok is false (silently) when the
// directory has no SKILL.md, or (with a logged warning) when its frontmatter is unparseable. The
// skill's bundled-file listing is folded into the body so skill_load surfaces it — the model learns
// what it can skill_read only after loading the skill.
func loadOne(skillDir, dirName string, diag *agentkit.Diagnostics) (agentkit.Skill, bool) {
	data, err := os.ReadFile(filepath.Join(skillDir, SkillFile))
	if err != nil {
		return agentkit.Skill{}, false // no SKILL.md → not a skill directory, silently ignore
	}
	if len(data) > maxBodyBytes {
		data = data[:maxBodyBytes]
	}
	m, body, err := frontmatter.Parse(data)
	if err != nil {
		discovery.Diagnose(diag, "skill:"+dirName, "skipped (unparseable SKILL.md): "+err.Error())
		return agentkit.Skill{}, false
	}

	// Skills are the deliberate exception to folder-is-identity: the agentskills.io standard
	// puts the canonical name in SKILL.md and a skill carries ZERO authority (no credential
	// owner, no shard key), so the frontmatter name wins and the folder is only the fallback.
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = dirName
	}
	body = strings.TrimSpace(body)
	if listing := resourceListing(skillDir); listing != "" {
		body += listing
	}
	return agentkit.Skill{
		Name:        name,
		Description: strings.TrimSpace(m.Description),
		Body:        body,
	}, true
}

// resourceListing renders a listing of a skill's bundled files (everything under its dir except
// SKILL.md), appended to the body so a loaded skill tells the model what it can skill_read. Empty if
// the skill bundles nothing.
func resourceListing(dir string) string {
	var files []string
	root, _ := filepath.Abs(dir)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || len(files) >= maxResourceListing {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == SkillFile {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if len(files) == 0 {
		return ""
	}
	sort.Strings(files)
	var b strings.Builder
	b.WriteString("\n\n<skill_resources note=\"read with skill_read; not loaded yet\">\n")
	for _, f := range files {
		b.WriteString(f + "\n")
	}
	b.WriteString("</skill_resources>")
	return b.String()
}
