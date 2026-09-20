// Package skill is nocturn's consumer-side skills layer: it reads the SKILL.md an extension carries
// (YAML frontmatter + Markdown body) into an agentkit.SkillSet, and provides skill_read for a skill's
// bundled files (Tier 3 of progressive disclosure). agentkit owns Tiers 1+2 (the catalog in the
// system prompt and skill_load); this package is the source adapter agentkit deliberately leaves to
// the consumer, plus the confined file reader.
//
// It walks the extensions tree and ignores every folder that carries no SKILL.md — a plugin's folder,
// a server's — because what a folder IS is read off the files in it (see internal/extension).
//
// A skill BODY is context, never authority: it shapes HOW the model uses its gated tools. Its
// folder's manifest is a different matter — that can declare a credential, which is why the body may
// reference {{config.…}} values and why an unconfigured skill says so instead of pretending.
package skill

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/discovery"
	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/frontmatter"
	"github.com/efuturetoday/nocturn/internal/secret"
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

// Discover scans dir (a workspace's extensions/ folder) for immediate subdirectories that contain a
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

	// The frontmatter name is what the model calls the skill; the FOLDER is what the workspace owns a
	// credential under. The agentskills.io standard puts the canonical name in SKILL.md, so it still
	// wins for the catalog, and the folder is the fallback — but a skill that declares a credential is
	// addressed as skill:<folder>, never as skill:<whatever the body claims to be called>.
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = dirName
	}
	body = strings.TrimSpace(body)

	// A skill may declare config — the address of the household's own server, say — and the values
	// are substituted here, once, on the way into the catalog. Unset config does NOT hide the skill:
	// the model still sees that it exists and learns the one command that finishes it, exactly as a
	// plugin's tools are exposed before its account is connected. A body full of unrendered
	// placeholders would read as a working skill and behave like a broken one.
	decl, err := extension.LoadDecl(skillDir)
	if err != nil {
		discovery.Diagnose(diag, "skill:"+dirName, "skipped (bad manifest): "+err.Error())
		return agentkit.Skill{}, false
	}
	// A config that does not survive validation is treated as an UNSET one, not as a broken skill:
	// the value is refused (nothing malformed reaches the prompt) and the body becomes the setup
	// notice. Skipping the skill instead would make it vanish from the catalog after a declaration
	// changed under an existing config — and a vanished skill is exactly what setupNotice exists to
	// prevent, since the assistant then denies it can do the thing at all.
	values, err := extension.LoadValues(skillDir, decl)
	if err != nil {
		discovery.Diagnose(diag, "skill:"+dirName, "config ignored: "+err.Error())
		values = extension.Values{}
	}
	if rendered, err := decl.Render(body, values); err != nil {
		discovery.Diagnose(diag, "skill:"+dirName, "not configured: "+err.Error())
		body = setupNotice(dirName, decl, values)
	} else {
		body = rendered
	}

	if listing := resourceListing(skillDir); listing != "" {
		body += listing
	}
	return agentkit.Skill{
		Name:        name,
		Description: strings.TrimSpace(m.Description),
		Body:        body,
	}, true
}

// setupNotice is the body a skill gets while its config is incomplete: what it needs, and the one
// command that supplies it. It replaces the real body rather than sitting beside it — the real body
// talks about an address that is not known yet, and half of it would be instructions for calling a
// server the workspace cannot name.
func setupNotice(folder string, d extension.Decl, v extension.Values) string {
	missing, _ := d.Missing(v, nil)
	var b strings.Builder
	b.WriteString("This skill is installed but not configured yet, so it cannot be used.\n\n")
	if len(missing) > 0 {
		b.WriteString("Missing settings: " + strings.Join(missing, ", ") + "\n\n")
		for _, c := range d.Config {
			if !slices.Contains(missing, c.Name) {
				continue
			}
			label := c.Label
			if label == "" {
				label = c.Name
			}
			b.WriteString("- " + c.Name + " — " + label)
			if c.Example != "" {
				b.WriteString(" (e.g. " + c.Example + ")")
			}
			b.WriteString("\n")
		}
		b.WriteString("\nTell the user to run: nocturn config " + folder + "\n")
	}
	b.WriteString("\nSay this plainly when the user asks for what this skill does; do not guess the missing values.")
	return b.String()
}

// controlPlane reports whether a skill-relative path is part of the extension control plane rather
// than a bundled resource: the declaration, the values a human supplied, the encrypted shard. They
// live in the same folder as the skill's own files, and none of them is material the model should
// list or read — the shard because it is credential material, the other two because they are the
// answer to "what may this skill do", which is a question the model does not get to research.
func controlPlane(rel string) bool {
	// Case-FOLDED, because the deny list is compared against a name the caller chose while the
	// filesystem underneath may not care: on a case-insensitive volume (APFS by default) os.Root
	// happily opens "MANIFEST.JSON", and an exact string switch would wave it through.
	switch strings.ToLower(rel) {
	case extension.ManifestFile, extension.ConfigFile, secret.ShardFile:
		return true
	}
	return false
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
		if err != nil || rel == SkillFile || controlPlane(rel) {
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
