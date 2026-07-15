// Package filecap is the filesystem capability group: file.read and file.write
// over a single workspace root. It is the second capability family after netcap
// and the proof that the broker's (capability, target) model is not HTTP-shaped:
// here the target is a PATH, glob-matched exactly like a host is (path.Match's
// "*" does not cross "/", so a ceiling like file.write @ notes/* is depth-bounded
// for free). Every call is confined to Root — a path that would escape is a hard
// error before the broker is even consulted — and then gated by the Guard, so an
// effect passes broker + HITL + the user's grants just like an HTTP call.
package filecap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// maxFileBytes bounds a single read so a huge file can't blow up memory or the
// model context; the brain truncates further for the model, but a script's
// nocturn.call would otherwise get the whole thing.
const maxFileBytes = 1 << 20 // 1 MiB

// Files is the filesystem capability group: it holds the shared Guard and the one
// workspace Root every path is confined to. Like netcap.Net, it is a small type
// with a *Guard plus its own dep (the root) — a new capability, not a new field
// on a god-object.
type Files struct {
	Guard *gateway.Guard
	Root  string
}

// New builds the filesystem capability group over guard, confining every path to
// root.
func New(guard *gateway.Guard, root string) *Files {
	return &Files{Guard: guard, Root: root}
}

// Tools exposes file.read and file.write as model/script/plugin tools.
func (f *Files) Tools() []tool.Tool {
	return []tool.Tool{f.readTool(), f.writeTool()}
}

func (f *Files) readTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "file.read",
			Description: "Read a UTF-8 text file from the workspace and return its contents.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Workspace-relative path to read"}},"required":["path"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			abs, target, err := f.resolve(args)
			if err != nil {
				return "", err
			}
			// Target is the workspace-relative path — the string the broker gates on.
			call := capability.Call{Capability: "file.read", Target: target}
			return gateway.Do(ctx, f.Guard, call, "read "+target, func() (string, error) {
				data, err := os.ReadFile(abs)
				if err != nil {
					return "", err
				}
				if len(data) > maxFileBytes {
					data = data[:maxFileBytes]
				}
				return string(data), nil
			})
		},
	}
}

func (f *Files) writeTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "file.write",
			Description: "Write a UTF-8 text file to the workspace (creating parent directories). This is a write and may require approval.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"path":{"type":"string","description":"Workspace-relative path to write"},` +
				`"content":{"type":"string","description":"File contents"}` +
				`},"required":["path","content"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			abs, target, err := f.resolvePath(a.Path)
			if err != nil {
				return "", err
			}
			call := capability.Call{Capability: "file.write", Target: target}
			intent := fmt.Sprintf("write %s (%d bytes)", target, len(a.Content))
			return gateway.Do(ctx, f.Guard, call, intent, func() (string, error) {
				if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
					return "", err
				}
				if err := os.WriteFile(abs, []byte(a.Content), 0o600); err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), target), nil
			})
		},
	}
}

// resolve parses the read tool's {path} argument and confines it.
func (f *Files) resolve(args string) (abs, target string, err error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", "", fmt.Errorf("invalid arguments: %w", err)
	}
	return f.resolvePath(a.Path)
}

// resolvePath confines userPath to Root and returns the absolute path plus the
// workspace-relative, forward-slashed target the broker gates on. A path that
// escapes the workspace (via .. or an absolute path outside Root) is a hard error
// — enforced HERE, before the broker, so confinement never depends on a rule
// being present (defense in depth: the ceiling scopes WITHIN the workspace; this
// guarantees there IS no outside).
func (f *Files) resolvePath(userPath string) (abs, target string, err error) {
	if strings.TrimSpace(userPath) == "" {
		return "", "", errors.New("missing required field: path")
	}
	rootAbs, err := filepath.Abs(f.Root)
	if err != nil {
		return "", "", err
	}
	abs = filepath.Join(rootAbs, filepath.FromSlash(userPath))
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("filecap: path %q escapes the workspace", userPath)
	}
	return abs, filepath.ToSlash(rel), nil
}
