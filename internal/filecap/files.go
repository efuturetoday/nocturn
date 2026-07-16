// Package filecap is the filesystem capability group: file.read and file.write
// over a single workspace root. It is the second capability family after netcap
// and the proof that the broker's (capability, target) model is not HTTP-shaped:
// here the target is a PATH, glob-matched exactly like a host is (path.Match's
// "*" does not cross "/", so a cage like file.write @ notes/* is depth-bounded
// for free). Every call is confined to Root — a path that would escape is a hard
// error before the broker is even consulted — and then gated by the Guard, so an
// effect passes broker + HITL + the user's grants just like an HTTP call.
package filecap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
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

// maxSearchResults bounds file.search so a broad glob over a large tree can't
// flood the model context or memory; the tail is dropped and flagged in the result.
const maxSearchResults = 500

// Tools exposes the filesystem capability as model/script/plugin tools. read,
// list, stat and search are observations (Write:false → run silently under base
// policy); write, remove and move are mutations (Write:true → ask). Every tool
// confines its path to Root before the broker and then passes through the Guard.
func (f *Files) Tools() []tool.Tool {
	return []tool.Tool{
		f.readTool(), f.writeTool(), f.listTool(), f.statTool(),
		f.removeTool(), f.searchTool(), f.moveTool(),
	}
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
			call := capability.Call{Family: "file", Write: false, Target: target}
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
			call := capability.Call{Family: "file", Write: true, Target: target}
			intent := fmt.Sprintf("write %s (%d bytes)", target, len(a.Content))
			return gateway.Do(ctx, f.Guard, call, intent, func() (string, error) {
				if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
					return "", err
				}
				if err := os.WriteFile(abs, []byte(a.Content), 0o600); err != nil {
					return "", err
				}
				return jsonResult(struct {
					Path         string `json:"path"`
					BytesWritten int    `json:"bytesWritten"`
				}{target, len(a.Content)})
			})
		},
	}
}

func (f *Files) listTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "file.list",
			Description: "List the entries of a workspace directory. Returns a JSON array of {name, isDir, size}.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Workspace-relative directory (default the workspace root)"}}}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			p := strings.TrimSpace(a.Path)
			if p == "" {
				p = "." // listing the workspace root is legal; target is "."
			}
			abs, target, err := f.resolvePath(p)
			if err != nil {
				return "", err
			}
			call := capability.Call{Family: "file", Write: false, Target: target}
			return gateway.Do(ctx, f.Guard, call, "list "+target, func() (string, error) {
				entries, err := os.ReadDir(abs)
				if err != nil {
					return "", err
				}
				type item struct {
					Name  string `json:"name"`
					IsDir bool   `json:"isDir"`
					Size  int64  `json:"size"`
				}
				out := make([]item, 0, len(entries))
				for _, e := range entries {
					it := item{Name: e.Name(), IsDir: e.IsDir()}
					if info, ierr := e.Info(); ierr == nil {
						it.Size = info.Size()
					}
					out = append(out, it)
				}
				b, _ := json.Marshal(out)
				return string(b), nil
			})
		},
	}
}

func (f *Files) statTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "file.stat",
			Description: "Stat a workspace path. Returns JSON {exists, isDir, size}; a missing path returns {\"exists\":false}.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Workspace-relative path to stat"}},"required":["path"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			abs, target, err := f.resolve(args)
			if err != nil {
				return "", err
			}
			call := capability.Call{Family: "file", Write: false, Target: target}
			return gateway.Do(ctx, f.Guard, call, "stat "+target, func() (string, error) {
				type stat struct {
					Exists bool  `json:"exists"`
					IsDir  bool  `json:"isDir"`
					Size   int64 `json:"size"`
				}
				info, err := os.Stat(abs)
				var s stat
				switch {
				case err == nil:
					s = stat{Exists: true, IsDir: info.IsDir(), Size: info.Size()}
				case errors.Is(err, fs.ErrNotExist):
					s = stat{Exists: false}
				default:
					return "", err
				}
				b, _ := json.Marshal(s)
				return string(b), nil
			})
		},
	}
}

func (f *Files) removeTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "file.remove",
			Description: "Remove a file (or empty directory) from the workspace. This is a write and may require approval.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Workspace-relative path to remove"}},"required":["path"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			abs, target, err := f.resolve(args)
			if err != nil {
				return "", err
			}
			call := capability.Call{Family: "file", Write: true, Target: target}
			return gateway.Do(ctx, f.Guard, call, "remove "+target, func() (string, error) {
				if err := os.Remove(abs); err != nil {
					return "", err
				}
				return jsonResult(struct {
					Path    string `json:"path"`
					Removed bool   `json:"removed"`
				}{target, true})
			})
		},
	}
}

func (f *Files) searchTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name: "file.search",
			Description: "Find files in the workspace by glob pattern, walking subdirectories. " +
				"A pattern with no '/' matches file names anywhere (e.g. \"*.md\"); a pattern " +
				"with a '/' matches the path relative to the search root. Returns a JSON array of paths.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"pattern":{"type":"string","description":"Glob pattern, e.g. *.md or src/*.go"},` +
				`"path":{"type":"string","description":"Workspace-relative directory to search under (default the workspace root)"}` +
				`},"required":["pattern"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(a.Pattern) == "" {
				return "", errors.New("missing required field: pattern")
			}
			// Validate the pattern up front so a bad glob is a clear error, not a
			// silent empty result deep in the walk.
			if _, err := path.Match(a.Pattern, ""); err != nil {
				return "", fmt.Errorf("invalid pattern %q: %w", a.Pattern, err)
			}
			base := strings.TrimSpace(a.Path)
			if base == "" {
				base = "." // searching from the workspace root
			}
			absBase, target, err := f.resolvePath(base)
			if err != nil {
				return "", err
			}
			recursive := !strings.Contains(a.Pattern, "/")
			// Gated on the search root as a read: the reach a cage/grant scopes is the
			// directory being walked, exactly as list gates on the directory.
			call := capability.Call{Family: "file", Write: false, Target: target}
			return gateway.Do(ctx, f.Guard, call, "search "+a.Pattern+" in "+target, func() (string, error) {
				rootAbs, err := filepath.Abs(f.Root)
				if err != nil {
					return "", err
				}
				matches := make([]string, 0, 16)
				truncated := false
				walkErr := filepath.WalkDir(absBase, func(p string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if d.IsDir() {
						return nil
					}
					// name-vs-path: a slashless pattern matches the base name at any depth;
					// a pattern with a slash matches the path relative to the search base.
					var candidate string
					if recursive {
						candidate = d.Name()
					} else {
						rel, rerr := filepath.Rel(absBase, p)
						if rerr != nil {
							return nil
						}
						candidate = filepath.ToSlash(rel)
					}
					ok, merr := path.Match(a.Pattern, candidate)
					if merr != nil {
						return merr
					}
					if !ok {
						return nil
					}
					// Report the workspace-relative path (from Root) so the result is a
					// path the other file.* tools accept directly.
					rel, rerr := filepath.Rel(rootAbs, p)
					if rerr != nil {
						return nil
					}
					matches = append(matches, filepath.ToSlash(rel))
					if len(matches) >= maxSearchResults {
						truncated = true
						return fs.SkipAll
					}
					return nil
				})
				if walkErr != nil {
					return "", walkErr
				}
				b, _ := json.Marshal(matches)
				out := string(b)
				if truncated {
					// Never let a capped sweep read as "found everything" (project rule:
					// no silent truncation).
					out = fmt.Sprintf("%s\n(truncated at %d results)", out, maxSearchResults)
				}
				return out, nil
			})
		},
	}
}

func (f *Files) moveTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "file.move",
			Description: "Move or rename a file within the workspace. This is a write and may require approval.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"from":{"type":"string","description":"Workspace-relative source path"},` +
				`"to":{"type":"string","description":"Workspace-relative destination path"}` +
				`},"required":["from","to"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				From string `json:"from"`
				To   string `json:"to"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			// BOTH endpoints are confined to Root before the broker — a move can neither
			// read from nor write outside the workspace.
			fromAbs, fromTarget, err := f.resolvePath(a.From)
			if err != nil {
				return "", err
			}
			toAbs, toTarget, err := f.resolvePath(a.To)
			if err != nil {
				return "", err
			}
			// Gated on the destination (the write) as the target a grant/cage scopes;
			// the intent names both endpoints so a human sees the whole move.
			call := capability.Call{Family: "file", Write: true, Target: toTarget}
			intent := fmt.Sprintf("move %s → %s", fromTarget, toTarget)
			return gateway.Do(ctx, f.Guard, call, intent, func() (string, error) {
				if err := os.MkdirAll(filepath.Dir(toAbs), 0o700); err != nil {
					return "", err
				}
				if err := os.Rename(fromAbs, toAbs); err != nil {
					return "", err
				}
				return jsonResult(struct {
					From string `json:"from"`
					To   string `json:"to"`
				}{fromTarget, toTarget})
			})
		},
	}
}

// jsonResult marshals a tool's structured result to the JSON-as-string every
// tool returns over the bus. Reads (list/stat/search) already return JSON; the
// mutations use this so their result is structured too (a script can read a field
// instead of parsing a sentence). Failure still rides the error channel, so the
// returned string is always a success payload.
func jsonResult(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
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
// being present (defense in depth: the cage scopes WITHIN the workspace; this
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
