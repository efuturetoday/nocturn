package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/efuturetoday/nocturn/agentkit"
)

const readToolDescription = "Read one of your own memory files by its path relative to the memory " +
	"folder, e.g. people/lina.md. The memory index in your system prompt links the detail files; " +
	"read one when the index line alone is not enough. Read-only, memory files only."

// readTool builds memory_read. Reading back text the assistant itself stored is context ingestion,
// not an external effect — zero authority — so it is NOT gated, exactly like skill_read. Confinement
// is enforced by os.Root: a path can never escape the memory folder, whether via "..", an absolute
// path, or a symlink pointing out (openat2 / RESOLVE_BENEATH — kernel-enforced, not string
// arithmetic), so it can never become a generic filesystem probe.
func (s *Store) readTool() (agentkit.Tool, error) {
	return agentkit.NewTool(
		"memory_read",
		readToolDescription,
		func(_ context.Context, args string) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			target, err := s.confine(a.Path)
			if err != nil {
				return "", err
			}

			// os.Root opens ONLY the memory folder; every operation through it is confined below that
			// directory at the syscall level. An escaping path fails here, before any byte is read.
			root, err := os.OpenRoot(s.dir)
			if err != nil {
				return "", err
			}
			defer root.Close()

			f, err := root.Open(filepath.FromSlash(target))
			if err != nil {
				return "", err
			}
			defer f.Close()

			info, err := f.Stat()
			if err != nil {
				return "", err
			}
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("%q is not a regular file", target)
			}

			data, err := io.ReadAll(io.LimitReader(f, maxDetailBytes))
			if err != nil {
				return "", err
			}
			// A memory file could hold a known vault secret (written before the value was stored, or
			// pasted in by hand); strip it before the content reaches the model, like file_read does.
			if s.scanner != nil {
				data = s.scanner.RedactIngress(data)
			}
			return string(data), nil
		},
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("path", agentkit.String("Path relative to the memory folder, e.g. people/lina.md")),
		).Require("path")),
		agentkit.WithMaxChars(maxDetailBytes),
	)
}
