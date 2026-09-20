package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/frontmatter"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// disabledDir holds the skills a person switched off. A dot-directory, so Discover's own skip keeps
// them out of the catalog without anything else having to know they exist.
//
// Off rather than gone is the point: a skill is a folder someone assembled, possibly with bundled
// files beside its SKILL.md, and "try without it for a week" should not mean fetching it again.
const disabledDir = ".disabled"

// Entry is one skill on disk as a person sees it — including the ones switched off, which Discover
// deliberately does not return.
type Entry struct {
	Name        string // the skill's own name: frontmatter first, folder as fallback
	Folder      string // the directory it lives in, relative to the extensions tree
	Description string
	Enabled     bool
	Bytes       int // size of SKILL.md, so a listing can say how much context it would cost
	// PartOf is set when the folder carries more than instructions — code, or a server declaration.
	// Such a skill is not a thing of its own to remove or switch off: doing either would take the
	// artifact, the declaration and the credentials in that folder with it. Empty for a plain skill.
	PartOf string
}

// List reports every skill under dir (a workspace's extensions/ folder), enabled and disabled,
// sorted by name. A directory without a readable SKILL.md is not a skill and is left out.
func List(dir string) ([]Entry, error) {
	out, err := listIn(dir, true)
	if err != nil {
		return nil, err
	}
	off, err := listIn(filepath.Join(dir, disabledDir), false)
	if err != nil {
		return nil, err
	}
	out = append(out, off...)
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// listIn reads one directory level into entries. A missing directory is no skills, not an error.
func listIn(dir string, enabled bool) ([]Entry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), SkillFile))
		if err != nil {
			continue // no SKILL.md → not a skill directory
		}
		m, _, err := frontmatter.Parse(data)
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name:        nameOf(m, e.Name()),
			Folder:      e.Name(),
			Description: strings.TrimSpace(m.Description),
			Enabled:     enabled,
			Bytes:       len(data),
			PartOf:      partOf(filepath.Join(dir, e.Name())),
		})
	}
	return out, nil
}

// otherPayloads are the files that make a folder more than a skill. Named here rather than imported
// from internal/extension, because this package must not depend on the composition side: what it
// needs to know is only "is there something else in here", and the answer is a stat.
var otherPayloads = []string{"plugin.json", "mcp.json"}

// partOf reports the extension a skill folder belongs to when that folder carries another payload —
// the folder's own name, since that is what an extension is called. Empty for a plain skill.
func partOf(dir string) string {
	for _, file := range otherPayloads {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			return filepath.Base(dir)
		}
	}
	return ""
}

// nameOf applies the same identity rule Discover does: the frontmatter name wins, the folder is the
// fallback. Skills are the deliberate exception to folder-is-identity in this tree — a skill carries
// no credential owner and no shard key, so nothing is pinned to where it sits.
// Parse turns a SKILL.md body into a validated skill, naming it from its frontmatter and falling
// back to fallbackName (a folder, usually) when the frontmatter does not.
//
// It exists because a skill body no longer arrives only as an extension folder's own SKILL.md: a
// plugin may bundle one, saying WHEN to reach for the tools it brings. Both paths must agree on what a skill is,
// down to the error text, so both call this.
func Parse(body, fallbackName string) (agentkit.Skill, error) {
	m, _, err := frontmatter.Parse([]byte(body))
	if err != nil {
		return agentkit.Skill{}, fmt.Errorf("skill %q: unparseable SKILL.md: %w", fallbackName, err)
	}
	sk := agentkit.Skill{
		Name:        nameOf(m, fallbackName),
		Description: strings.TrimSpace(m.Description),
		Body:        body,
	}
	if err := sk.Validate(); err != nil {
		return agentkit.Skill{}, fmt.Errorf("skill %q: %w", fallbackName, err)
	}
	return sk, nil
}

func nameOf(m frontmatter.Meta, folder string) string {
	if n := strings.TrimSpace(m.Name); n != "" {
		return n
	}
	return folder
}

// Read returns a skill's SKILL.md verbatim — frontmatter included, because reviewing a skill means
// reading what the model will be told, and the frontmatter is part of that.
func Read(dir, name string) (string, error) {
	e, err := find(dir, name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(pathOf(dir, e), SkillFile))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Remove deletes a skill's directory, enabled or disabled.
//
// No trash here, unlike a workspace, and the difference is what is lost: a workspace folder holds
// conversations and a vault that exist nowhere else, while a skill is instructions that came from
// somewhere and can come from there again.
func Remove(dir, name string) error {
	e, err := find(dir, name)
	if err != nil {
		return err
	}
	if e.PartOf != "" {
		return fmt.Errorf("%q belongs to the extension %q, which also brings code or a server — "+
			"remove the extension instead of the skill inside it", name, e.PartOf)
	}
	// A DISABLED skill sits under the dot-directory, which is not an extension folder — deleting it
	// is a plain path removal. An enabled one is the extension, and goes through the one remover.
	if !e.Enabled {
		return os.RemoveAll(pathOf(dir, e))
	}
	return extension.Remove(dir, e.Folder)
}

// FolderOf resolves a skill NAME to the directory it lives in. The two differ — the frontmatter name
// wins for the catalog while the folder is what owns the credential and the shard — so anything
// reaching for a skill's extension identity has to ask for it rather than assume.
func FolderOf(dir, name string) (string, bool) {
	e, err := find(dir, name)
	if err != nil {
		return "", false
	}
	return e.Folder, true
}

// SetEnabled moves a skill between extensions/ and extensions/.disabled/, which is the whole
// mechanism: the catalog is what Discover finds, and Discover skips dot-directories.
//
// A move rather than a flag in the frontmatter, because the frontmatter is the skill's own file —
// the agentskills.io format, shared with whoever published it. Writing our own field into someone
// else's document would make a skill that travels badly, and frontmatter.Meta deliberately knows
// only Name and Description.
func SetEnabled(dir, name string, on bool) error {
	e, err := find(dir, name)
	if err != nil {
		return err
	}
	if e.Enabled == on {
		return nil
	}
	if e.PartOf != "" {
		return fmt.Errorf("%q belongs to the extension %q, which also brings code or a server — "+
			"switching it off would take those out of service too", name, e.PartOf)
	}
	// A secret shard is keyed and AAD-bound to its FOLDER PATH, and switching off moves the folder.
	// Its credentials would then be unreadable until it came back — and unreadable is indistinguishable
	// from gone, which is not what "switched off" should mean.
	if _, err := os.Stat(filepath.Join(pathOf(dir, e), secret.ShardFile)); err == nil {
		return fmt.Errorf("%q holds a credential, and switching it off would move its folder, which is "+
			"what its credentials are encrypted against — remove it instead, or delete the credential first", name)
	}
	from := pathOf(dir, e)
	to := filepath.Join(dir, e.Folder)
	if !on {
		to = filepath.Join(dir, disabledDir, e.Folder)
		if err := os.MkdirAll(filepath.Join(dir, disabledDir), 0o700); err != nil {
			return err
		}
	}
	if _, err := os.Stat(to); err == nil {
		return fmt.Errorf("skill %q: %q already exists on the other side", name, e.Folder)
	}
	return os.Rename(from, to)
}

// Write installs a skill: its body becomes extensions/<folder>/SKILL.md.
//
// It refuses a name that is already taken, rather than overwriting. Discover drops a duplicate name
// silently (first wins), so installing into a shadow would look like it worked and change nothing —
// which is the worst of the three possible outcomes.
func Write(dir, folder, body, manifest string) (Entry, error) {
	sk, err := Parse(body, folder)
	if err != nil {
		return Entry{}, err
	}
	// A declaration is validated BEFORE anything lands, not when the workspace next reads it. A
	// manifest that parses but does not validate would install "successfully" and then make the whole
	// skill disappear at discovery — the opposite of the rule next door, which is that an unfinished
	// extension stays visible and says what it needs.
	if manifest != "" {
		if _, err := extension.ParseDecl([]byte(manifest)); err != nil {
			return Entry{}, fmt.Errorf("skill %q: %w", folder, err)
		}
	}
	if existing, err := find(dir, sk.Name); err == nil {
		return Entry{}, fmt.Errorf("skill %q already exists in %q", sk.Name, existing.Folder)
	}

	target := filepath.Join(dir, folder)
	if _, err := os.Stat(target); err == nil {
		return Entry{}, fmt.Errorf("extensions/%s already exists", folder)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return Entry{}, err
	}
	// The declaration lands FIRST, and the body last. Discovery keys on the body — a folder without a
	// SKILL.md is not a skill — so a crash between the two writes leaves something that is not
	// discovered at all, rather than a skill that renders and silently binds nothing because its
	// declaration never arrived. The declaration is written by the install and never by a person
	// editing afterwards; config.json is the file a person edits.
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(target, extension.ManifestFile), []byte(manifest), 0o600); err != nil {
			return Entry{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(target, SkillFile), []byte(body), 0o600); err != nil {
		return Entry{}, err
	}
	return Entry{
		Name:        sk.Name,
		Folder:      folder,
		Description: sk.Description,
		Enabled:     true,
		Bytes:       len(body),
	}, nil
}

// find resolves a skill NAME to its entry.
//
// Addressing by name and resolving to a folder is not indirection for its own sake: the frontmatter
// name wins over the folder here (see nameOf), so a skill called "deploy" may live in a directory
// called anything at all. A caller that treated the name as a path would fail to find that skill —
// or, worse, hit a different one.
func find(dir, name string) (Entry, error) {
	all, err := List(dir)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range all {
		if e.Name == name {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("no skill %q", name)
}

// pathOf is where an entry's directory actually is.
func pathOf(dir string, e Entry) string {
	if e.Enabled {
		return filepath.Join(dir, e.Folder)
	}
	return filepath.Join(dir, disabledDir, e.Folder)
}
