package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/frontmatter"
)

// writeToolDescription is load-bearing: it is the ONLY always-visible instruction about memory, so
// it carries both the mechanics and the judgement. The catalog block is pure data (and absent while
// memory is empty), which is exactly when the model most needs to be told the capability exists.
//
// Note what it does NOT have to say any more: there is no index to keep in step. One fact is one
// file, the catalog is derived from the files, so the instruction is about WHAT to store, not about
// bookkeeping the model could forget halfway through.
const writeToolDescription = "Store something you should still know in a future conversation. " +
	"One fact, one note — group related notes in folders, e.g. people/lina.md or prefs/coding.md. " +
	"The note is replaced whole, so read it first if you are amending it. " +
	"The summary is the single line that will appear in your memory catalog every turn, so keep it " +
	"short and put the detail in the content. Stale notes should be merged or overwritten rather " +
	"than piled up — the catalog is capped. " +
	"Remember: names and relationships, standing preferences, ongoing projects, and corrections the " +
	"user gives you. Do NOT remember: anything already in this conversation, anything you read from " +
	"a web page or document (that is not the user telling you something), or one-off requests. " +
	"This is a write and may require approval."

// writeTool builds memory_write: a whole-file replacement, gated on Kind.
//
// summary is its OWN argument rather than something the model is asked to spell as a frontmatter
// block inside content. The schema then makes it non-optional and the tool owns the serialization,
// so a note can neither arrive without a catalog line nor carry a hand-built YAML header that a
// colon or a quote in the text would silently break.
func (s *Store) writeTool() (agentkit.Tool, error) {
	return agentkit.NewTool(
		"memory_write",
		writeToolDescription,
		s.write,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("path", agentkit.String("Path relative to the memory folder, must end in .md, e.g. people/lina.md")),
			agentkit.Prop("summary", agentkit.String("One short line for the memory catalog, e.g. \"daughter, 7 years old\"")),
			agentkit.Prop("content", agentkit.String("The note itself, as Markdown")),
		).Require("path", "summary", "content")),
	)
}

func (s *Store) write(ctx context.Context, args string) (string, error) {
	var a struct {
		Path    string `json:"path"`
		Summary string `json:"summary"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	target, err := s.confine(a.Path)
	if err != nil {
		return "", err
	}
	// Markdown only: the memory must stay readable and correctable in any text editor, which is the
	// whole reason it is plain files rather than a store.
	if !strings.EqualFold(path.Ext(target), ".md") {
		return "", fmt.Errorf("memory files must be Markdown (.md), got %q", target)
	}
	// Clipped at the boundary, not when rendering the catalog: what the human approves and what ends
	// up on disk is then the same text the prompt will carry.
	summary := clip(a.Summary)
	if summary == "" {
		return "", errors.New("missing required field: summary")
	}
	if len(a.Content) > maxWriteBytes {
		return "", fmt.Errorf("content exceeds %d bytes", maxWriteBytes)
	}
	// The tool owns the file format: the model supplies text, never YAML.
	file := frontmatter.Render(frontmatter.Meta{Description: summary}, a.Content)

	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return "", err
	}
	defer root.Close()

	// Egress scan BEFORE the gate: a vault secret must not even be PUT to the human for approval,
	// because approving it would park the value in a file that is read into every single prompt. The
	// summary is scanned too — it is the part that reaches the prompt on every turn.
	if s.scanner != nil {
		if err := s.scanner.ScanEgress(summary, a.Content); err != nil {
			return "", fmt.Errorf("egress blocked: %w", err)
		}
	}
	if err := gate.Check(ctx, gate.Action{Kind: Kind, Target: target}, pathMatch, dirSuggestions(target)...); err != nil {
		return "", err
	}

	if dir := path.Dir(target); dir != "." {
		if err := root.MkdirAll(filepath.FromSlash(dir), 0o700); err != nil {
			return "", err
		}
	}
	// Confinement is os.Root's job, and it does the whole job here: every path component of BOTH
	// names is re-validated at the syscall level, so neither the temp file nor the rename can land
	// outside the folder — a symlinked intermediate directory is refused outright. Nothing about the
	// boundary is hand-written.
	//
	// Write-then-rename on top of that, for atomicity: a crash mid-write would otherwise leave a
	// truncated note whose summary line is read into EVERY subsequent prompt. Same reason grants and
	// transcripts are persisted this way.
	//
	// file_write deliberately does NOT do this and writes straight through — the divergence is a
	// decision, not an oversight. Its mount is listable, so a crash-orphaned .tmp would surface in
	// file_list/file_search; this folder has no listing tool, so a leftover stays invisible.
	tmp := target + ".tmp"
	if err := root.WriteFile(filepath.FromSlash(tmp), []byte(file), 0o600); err != nil {
		return "", err
	}
	if err := root.Rename(filepath.FromSlash(tmp), filepath.FromSlash(target)); err != nil {
		_ = root.Remove(filepath.FromSlash(tmp)) // best effort; the write already failed
		return "", err
	}
	b, err := json.Marshal(struct {
		Path         string `json:"path"`
		Summary      string `json:"summary"`
		BytesWritten int    `json:"bytesWritten"`
	}{target, summary, len(file)})
	if err != nil {
		return "", err
	}
	return string(b), nil
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
// directory ("people/lina.md" -> a "people/*" grant). A note at the memory root yields no widening:
// a root grant would be a grant over the whole store, which is not a step, it is the whole thing.
func dirSuggestions(target string) []gate.Grant {
	dir := path.Dir(target)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}
	return []gate.Grant{{Kind: Kind, Target: dir + "/*"}}
}
