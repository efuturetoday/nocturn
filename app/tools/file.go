package tools

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

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// FileKind is the shared gate Kind file MUTATIONS check; the Target is the workspace-relative,
// forward-slashed path. read/list/stat/search are observations — confined to the workspace Root but
// ungated (they prompt nobody, matching the old base policy where reads ran silently); write/remove/
// move are mutations gated on this kind so they ask. Path matching (a "notes/*" grant covering that
// directory, path.Match's "*" not crossing "/") lives here with the tool that owns it.
const FileKind = "file"

const (
	maxFileBytes     = 1 << 20 // 1 MiB read cap, so a huge file can't blow up memory/context
	maxSearchResults = 500     // cap a broad glob; the tail is dropped and flagged
)

// files is the filesystem tool group: every path is confined to one workspace root before anything
// else. Like Net it is a small type owning its dep (the root), not a field on a god-object.
type files struct{ root string }

// Tools exposes the filesystem tools. read/list/stat/search are observations (ungated within Root);
// write/remove/move are mutations gated on FileKind.
func (f files) Tools() ([]agentkit.Tool, error) {
	specs := []struct {
		name, desc string
		schema     *agentkit.Schema
		fn         agentkit.ToolFunc
	}{
		{"file_read", "Read a UTF-8 text file from the workspace and return its contents.",
			agentkit.Object(agentkit.Prop("path", agentkit.String("Workspace-relative path to read"))).Require("path"), f.read},
		{"file_write", "Write a UTF-8 text file to the workspace (creating parent directories). This is a write and may require approval.",
			agentkit.Object(
				agentkit.Prop("path", agentkit.String("Workspace-relative path to write")),
				agentkit.Prop("content", agentkit.String("File contents")),
			).Require("path", "content"), f.write},
		{"file_list", "List the entries of a workspace directory. Returns a JSON array of {name, isDir, size}.",
			agentkit.Object(agentkit.Prop("path", agentkit.String("Workspace-relative directory (default the workspace root)"))), f.list},
		{"file_stat", `Stat a workspace path. Returns JSON {exists, isDir, size}; a missing path returns {"exists":false}.`,
			agentkit.Object(agentkit.Prop("path", agentkit.String("Workspace-relative path to stat"))).Require("path"), f.stat},
		{"file_remove", "Remove a file (or empty directory) from the workspace. This is a write and may require approval.",
			agentkit.Object(agentkit.Prop("path", agentkit.String("Workspace-relative path to remove"))).Require("path"), f.remove},
		{"file_search", "Find files in the workspace by glob pattern, walking subdirectories. A pattern with no '/' matches file names anywhere (e.g. \"*.md\"); a pattern with a '/' matches the path relative to the search root. Returns a JSON array of paths.",
			agentkit.Object(
				agentkit.Prop("pattern", agentkit.String("Glob pattern, e.g. *.md or src/*.go")),
				agentkit.Prop("path", agentkit.String("Workspace-relative directory to search under (default the workspace root)")),
			).Require("pattern"), f.search},
		{"file_move", "Move or rename a file within the workspace. This is a write and may require approval.",
			agentkit.Object(
				agentkit.Prop("from", agentkit.String("Workspace-relative source path")),
				agentkit.Prop("to", agentkit.String("Workspace-relative destination path")),
			).Require("from", "to"), f.move},
	}
	out := make([]agentkit.Tool, 0, len(specs))
	for _, s := range specs {
		t, err := agentkit.NewTool(s.name, s.desc, s.fn, agentkit.WithSchema(s.schema))
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (f files) read(_ context.Context, args string) (string, error) {
	abs, _, err := f.resolve(args)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if len(data) > maxFileBytes {
		data = data[:maxFileBytes]
	}
	return string(data), nil
}

func (f files) list(_ context.Context, args string) (string, error) {
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
	abs, _, err := f.resolvePath(p)
	if err != nil {
		return "", err
	}
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
	return jsonResult(out)
}

func (f files) stat(_ context.Context, args string) (string, error) {
	abs, _, err := f.resolve(args)
	if err != nil {
		return "", err
	}
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
	return jsonResult(s)
}

func (f files) search(ctx context.Context, args string) (string, error) {
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
	// Validate the pattern up front so a bad glob is a clear error, not a silent empty result.
	if _, err := path.Match(a.Pattern, ""); err != nil {
		return "", fmt.Errorf("invalid pattern %q: %w", a.Pattern, err)
	}
	base := strings.TrimSpace(a.Path)
	if base == "" {
		base = "." // searching from the workspace root
	}
	absBase, _, err := f.resolvePath(base)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(f.root)
	if err != nil {
		return "", err
	}
	recursive := !strings.Contains(a.Pattern, "/")
	matches := make([]string, 0, 16)
	truncated := false
	walkErr := filepath.WalkDir(absBase, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// name-vs-path: a slashless pattern matches the base name at any depth; a pattern with a
		// slash matches the path relative to the search base.
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
		// Report the workspace-relative path (from Root) so the result feeds the other file_ tools.
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
	b, err := json.Marshal(matches)
	if err != nil {
		return "", err
	}
	out := string(b)
	if truncated {
		// Never let a capped sweep read as "found everything" (no silent truncation).
		out = fmt.Sprintf("%s\n(truncated at %d results)", out, maxSearchResults)
	}
	return out, nil
}

func (f files) write(ctx context.Context, args string) (string, error) {
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
	if err := gate.Check(ctx, gate.Action{Kind: FileKind, Target: target}, pathMatch, dirSuggestions(target)...); err != nil {
		return "", err
	}
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
}

func (f files) remove(ctx context.Context, args string) (string, error) {
	abs, target, err := f.resolve(args)
	if err != nil {
		return "", err
	}
	if err := gate.Check(ctx, gate.Action{Kind: FileKind, Target: target}, pathMatch, dirSuggestions(target)...); err != nil {
		return "", err
	}
	if err := os.Remove(abs); err != nil {
		return "", err
	}
	return jsonResult(struct {
		Path    string `json:"path"`
		Removed bool   `json:"removed"`
	}{target, true})
}

func (f files) move(ctx context.Context, args string) (string, error) {
	var a struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	// BOTH endpoints are confined to Root — a move can neither read from nor write outside it.
	fromAbs, fromTarget, err := f.resolvePath(a.From)
	if err != nil {
		return "", err
	}
	toAbs, toTarget, err := f.resolvePath(a.To)
	if err != nil {
		return "", err
	}
	// Gated on the destination (the write) as the target a grant scopes.
	if err := gate.Check(ctx, gate.Action{Kind: FileKind, Target: toTarget}, pathMatch, dirSuggestions(toTarget)...); err != nil {
		return "", err
	}
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
}

// resolve parses a tool's {path} argument and confines it.
func (f files) resolve(args string) (abs, target string, err error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", "", fmt.Errorf("invalid arguments: %w", err)
	}
	return f.resolvePath(a.Path)
}

// resolvePath confines userPath to Root and returns the absolute path plus the workspace-relative,
// forward-slashed target the gate checks. A path that escapes the workspace (via .. or an absolute
// path outside Root) is a hard error — enforced HERE, before the gate, so confinement never depends
// on a rule being present.
func (f files) resolvePath(userPath string) (abs, target string, err error) {
	if strings.TrimSpace(userPath) == "" {
		return "", "", errors.New("missing required field: path")
	}
	rootAbs, err := filepath.Abs(f.root)
	if err != nil {
		return "", "", err
	}
	abs = filepath.Join(rootAbs, filepath.FromSlash(userPath))
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes the workspace", userPath)
	}
	return abs, filepath.ToSlash(rel), nil
}

// pathMatch reports whether a granted path pattern covers a target path, using path.Match glob
// semantics ("*" does not cross "/"), plus "*" for any.
func pathMatch(pattern, target string) bool {
	if pattern == "*" || pattern == target {
		return true
	}
	ok, err := path.Match(pattern, target)
	return err == nil && ok
}

// dirSuggestions offers the human one widening beyond the exact path: allow the whole containing
// directory ("notes/todo.md" -> a "notes/*" grant). A file at the workspace root yields no widening.
func dirSuggestions(target string) []gate.Grant {
	dir := path.Dir(target)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}
	return []gate.Grant{{Kind: FileKind, Target: dir + "/*"}}
}

// jsonResult marshals a tool's structured result to the JSON-as-string every tool returns.
func jsonResult(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
