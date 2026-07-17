package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/efuturetoday/nocturn/internal/tool"
)

// ReadToolName is the tool that reads a skill's bundled files (scripts,
// references, templates) — Tier 3 of progressive disclosure, on demand.
const ReadToolName = "skill.read"

// maxResourceBytes caps one resource read.
const maxResourceBytes = 256 << 10

// maxResourceListing bounds how many bundled files skill.load advertises, so a
// skill with a huge tree doesn't bloat the activation output.
const maxResourceListing = 40

// ReadTool builds skill.read: it returns a bundled file of an ALREADY-ACTIVATED
// skill by relative path. It is not routed through the broker — reading
// pre-installed instruction/asset text is context ingestion, not an external
// effect, and carries zero authority. It is confined to the skill's own
// directory (escape is a hard error) and read-only, so it can never become a
// generic filesystem probe. It is still observable: every call is emitted as a
// activity.ToolEvent through the Registry's activity sink.
func (ix *Index) ReadTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name: ReadToolName,
			Description: "Read a bundled file of a skill you have ALREADY loaded, by its path relative to " +
				"the skill: a reference the skill points you to (read it for context), a template or example " +
				"asset, or a script to run with code.run. Only the skill's own files, read-only.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"name":{"type":"string","description":"The loaded skill the file belongs to."},` +
				`"path":{"type":"string","description":"File path relative to the skill, e.g. scripts/extract.js"}` +
				`},"required":["name","path"]}`),
			MaxResult: maxResourceBytes,
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				Name string `json:"name"`
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			// A resource is readable only once its skill is loaded — this keeps the
			// tool from being used as a generic file probe and mirrors the standard's
			// "resources load when the instructions reference them".
			if act := ActiveFrom(ctx); act == nil || !act.Has(a.Name) {
				return "", fmt.Errorf("skill %q is not loaded; load it before reading its files", a.Name)
			}
			s, ok := ix.Get(a.Name)
			if !ok {
				return "", fmt.Errorf("unknown skill %q", a.Name)
			}
			abs, err := confine(s.Dir, a.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return "", err
			}
			if len(data) > maxResourceBytes {
				data = data[:maxResourceBytes]
			}
			return string(data), nil
		},
	}
}

// confine resolves rel under root and rejects any path that escapes the skill
// directory — lexically (via ..) and after symlink resolution — before any read.
func confine(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errors.New("skill: empty resource path")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = r
	}
	abs := filepath.Join(rootAbs, filepath.FromSlash(rel))
	if escapes(rootAbs, abs) {
		return "", fmt.Errorf("skill: path %q escapes the skill directory", rel)
	}
	// A symlink inside the skill dir could still point out; resolve and re-check.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		if escapes(rootAbs, resolved) {
			return "", fmt.Errorf("skill: path %q resolves outside the skill directory", rel)
		}
		abs = resolved
	}
	return abs, nil
}

func escapes(root, abs string) bool {
	r, err := filepath.Rel(root, abs)
	return err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// resourceListing renders a non-eager listing of a skill's bundled files (all
// files under its dir except SKILL.md), for appending to the load output so the
// model knows what it can skill.read without loading them eagerly. Empty if none.
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
	b.WriteString("\n<skill_resources note=\"read with skill.read; not loaded yet\">\n")
	for _, f := range files {
		b.WriteString(f + "\n")
	}
	b.WriteString("</skill_resources>")
	return b.String()
}
