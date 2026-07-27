// Package memory is the assistant's own durable memory: a per-workspace folder of Markdown files
// whose index (MEMORY.md) is folded into every system prompt, with the detail files loaded on
// demand. It is the counterpart to internal/skill — skills shape HOW the model works, memory holds
// WHAT it knows about its user.
//
// The folder is a sibling of the file tools' mount, not inside it (ADR-10): file_write can never
// reach it, so a prompt injection cannot rewrite the assistant's memory through a generic file tool.
// The only writer is memory_write, and that asks the human through the gate. Reading is ungated —
// pre-installed text is context ingestion, zero authority, exactly like skill_read.
package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/frontmatter"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// Kind is the gate Kind a memory MUTATION checks; the Target is the memory-relative, forward-slashed
// path, so a grant can be widened to a directory ("people/*"). Reads carry no Kind at all — they are
// not gated. The matcher lives here with the tool that owns it, like FileKind's in internal/tools.
const Kind = "memory"

const (
	// maxIndexBytes caps what reaches the system prompt. Deliberately tight: the index is paid for in
	// EVERY turn, forever. Because the index is DERIVED, this ceiling is enforced rather than
	// requested — entries past it are dropped and the drop is stated. Note files themselves are
	// uncapped; they cost nothing until read.
	maxIndexBytes = 2 << 10

	// maxDetailBytes caps one memory_read.
	maxDetailBytes = 64 << 10

	// maxWriteBytes caps one memory_write, so a runaway generation cannot fill the disk.
	maxWriteBytes = 64 << 10

	// maxSummaryLen bounds one index line, so a note with a rambling description cannot crowd out
	// every other fact.
	maxSummaryLen = 120

	// maxNotes bounds the folder walk, a fail-safe against a pathological tree.
	maxNotes = 500
)

// Store is one workspace's memory folder. A missing folder is not an error: the index is empty and
// the first write creates it.
type Store struct {
	dir     string
	scanner *secret.Scanner // egress guard on write, ingress redaction on read; nil = no scanning
	log     *slog.Logger    // reports an index that exists but cannot be read; nil = silent
}

// New builds the Store over dir (a workspace's memory/ folder). It touches no disk: Index reads on
// every call so an edit in a text editor lands in the next turn, and the tools open the folder per
// operation.
func New(dir string, scanner *secret.Scanner) *Store {
	return &Store{dir: dir, scanner: scanner, log: slog.New(slog.DiscardHandler)}
}

// SetLogger installs the logger Index reports an unreadable index through. Optional; without it the
// store is silent.
func (s *Store) SetLogger(l *slog.Logger) {
	if l != nil {
		s.log = l
	}
}

// Index renders the catalog folded into the system prompt: one line per note, "path — summary",
// sorted by path. It is DERIVED from the notes on disk, never a file of its own — so it can never
// drift from what is actually stored, the model has one thing to write instead of two, and the
// prompt budget below is enforced rather than politely requested.
//
// An empty folder yields "" — a fresh workspace pays no tokens at all, and the model still learns it
// can remember things from memory_write's description.
//
// It walks fresh on every call rather than caching: the tree is small, the model may have just
// written to it, and the human may have corrected a note in an editor between two turns.
func (s *Store) Index() string {
	notes := s.notes()
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, n := range notes {
		line := n.path + " — " + n.summary + "\n"
		// Enforced, not requested: stop at the budget and say how much was left out, so a full memory
		// reads as full instead of as complete.
		if b.Len()+len(line) > maxIndexBytes {
			fmt.Fprintf(&b, "(%d more notes not listed — memory is at its limit; consolidate)", len(notes)-i)
			break
		}
		b.WriteString(line)
	}
	return strings.TrimSpace(b.String())
}

// note is one catalog entry.
type note struct {
	path    string // memory-relative, forward-slashed
	summary string
}

// notes walks the folder for Markdown notes and reads a one-line summary from each: the frontmatter
// description if present, else the first non-empty body line. Anything unreadable is skipped with a
// warning rather than dropped silently — a note that exists but cannot be summarized must not make
// the assistant believe it never stored the fact.
func (s *Store) notes() []note {
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("memory: opening the memory folder, continuing without it", "err", err)
		}
		return nil
	}
	defer root.Close()

	var out []note
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory: skip it, but say so. Silence here would make a whole branch
			// of the memory vanish from the catalog with nothing to explain it.
			s.log.Warn("memory: walking a subdirectory, leaving it out of the catalog", "path", p, "err", err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if len(out) >= maxNotes {
			// Stop the walk rather than keep discarding — and say it, because a capped sweep must never
			// read as a complete one. Pathological enough to belong in the trace, not in the prompt.
			s.log.Warn("memory: too many notes, catalog truncated", "limit", maxNotes)
			return fs.SkipAll
		}
		if !strings.EqualFold(path.Ext(p), ".md") {
			return nil
		}
		data, err := fs.ReadFile(root.FS(), p)
		if err != nil {
			s.log.Warn("memory: reading a note, leaving it out of the catalog", "note", p, "err", err)
			return nil
		}
		out = append(out, note{path: p, summary: summarize(data)})
		return nil
	})
	if err != nil {
		s.log.Warn("memory: walking the memory folder", "err", err)
	}
	slices.SortFunc(out, func(a, b note) int { return strings.Compare(a.path, b.path) })
	return out
}

// summarize derives a note's one-line catalog entry: the frontmatter description, else the first
// non-empty line of the body with any Markdown heading marker stripped. Never empty — a note with
// nothing to say still has to appear, or the model would not know it exists.
func summarize(data []byte) string {
	body := string(data)
	if m, rest, err := frontmatter.Parse(data); err == nil {
		if d := strings.TrimSpace(m.Description); d != "" {
			return clip(d)
		}
		body = rest
	}
	for line := range strings.SplitSeq(body, "\n") {
		if t := strings.TrimSpace(strings.TrimLeft(line, "# ")); t != "" {
			return clip(t)
		}
	}
	return "(no summary)"
}

// clip bounds one catalog line to maxSummaryLen runes, so one rambling note cannot crowd out every
// other fact in the prompt.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ") // collapse newlines/tabs: one entry is one line
	if r := []rune(s); len(r) > maxSummaryLen {
		return strings.TrimSpace(string(r[:maxSummaryLen])) + "…"
	}
	return s
}

// Tools returns memory_read and memory_write. They are base tools like the file tools, so an agent
// reaches them only if its declared cage keeps them.
func (s *Store) Tools() ([]agentkit.Tool, error) {
	read, err := s.readTool()
	if err != nil {
		return nil, err
	}
	write, err := s.writeTool()
	if err != nil {
		return nil, err
	}
	return []agentkit.Tool{read, write}, nil
}

// confine returns the memory-relative, forward-slashed target for userPath and rejects an obvious
// lexical escape as a fast pre-check. It is NOT the security boundary — os.Root is, at each
// operation — but it yields the clean target the gate matches.
func (s *Store) confine(userPath string) (string, error) {
	if strings.TrimSpace(userPath) == "" {
		return "", errors.New("missing required field: path")
	}
	rootAbs, err := filepath.Abs(s.dir)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(rootAbs, filepath.FromSlash(userPath))
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// Logged like the file tools log theirs — and it matters more here: past this folder lie
		// grants.json, vault.enc and PERSONA.md, so an attempt is worth seeing in the trace.
		s.log.Warn("memory path escapes the memory folder", "path", userPath)
		return "", fmt.Errorf("path %q escapes the memory folder", userPath)
	}
	return filepath.ToSlash(rel), nil
}
