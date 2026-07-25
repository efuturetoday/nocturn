package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
)

const (
	readToolName = "skill_read"

	// maxResourceBytes caps one resource read.
	maxResourceBytes = 256 << 10

	readToolDescription = "Read a bundled file of a skill by its path relative to the skill: a " +
		"reference the skill points you to, a template or example asset, or a script to run with " +
		"code_run. Load the skill with skill_load first; the loaded skill lists its files. " +
		"Read-only, only the skill's own files."
)

// ReadTool builds skill_read over dirs (skill name → absolute directory). It returns a skill's
// bundled file. Reading pre-installed instruction/asset text is context ingestion, not an external
// effect — zero authority — so it is not gated. Confinement is enforced by os.Root: a path can NEVER
// escape the skill's own directory, whether via "..", an absolute path, or a symlink pointing out
// (openat2 / RESOLVE_BENEATH — kernel-enforced, not string arithmetic), so it can never become a
// generic filesystem probe.
func ReadTool(dirs map[string]string) (agentkit.Tool, error) {
	return agentkit.NewTool(
		readToolName,
		readToolDescription,
		func(_ context.Context, args string) (string, error) {
			var a struct {
				Name string `json:"name"`
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			skillDir, ok := dirs[a.Name]
			if !ok {
				return "", fmt.Errorf("unknown skill %q", a.Name)
			}
			rel := strings.TrimSpace(a.Path)
			if rel == "" {
				return "", errors.New("empty resource path")
			}

			// os.Root opens ONLY skillDir; every operation through it is confined below that
			// directory at the syscall level. An escaping path fails here, before any byte is read.
			root, err := os.OpenRoot(skillDir)
			if err != nil {
				return "", err
			}
			defer root.Close()

			f, err := root.Open(filepath.FromSlash(rel))
			if err != nil {
				return "", fmt.Errorf("skill %q: %w", a.Name, err)
			}
			defer f.Close()

			info, err := f.Stat()
			if err != nil {
				return "", err
			}
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("skill %q: %q is not a regular file", a.Name, rel)
			}

			data, err := io.ReadAll(io.LimitReader(f, maxResourceBytes))
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("name", agentkit.String("The skill the file belongs to.")),
			agentkit.Prop("path", agentkit.String("File path relative to the skill, e.g. scripts/analyze.js")),
		).Require("name", "path")),
		agentkit.WithMaxChars(maxResourceBytes),
	)
}
