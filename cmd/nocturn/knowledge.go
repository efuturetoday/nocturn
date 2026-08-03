package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/efuturetoday/nocturn/internal/knowledge"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// Managing a workspace's document index from the command line.
//
// The daemon reconciles on its own every minute, so none of this is required to use the feature —
// it is here for the two moments a schedule is the wrong shape: the first index of a folder, where
// somebody wants to watch it happen and see what it cost, and after changing the embedding model,
// where the answer is "delete the index and build it again" and a person should be the one deciding
// to spend that.

func cmdKnowledge(args []string) int {
	if len(args) == 0 {
		knowledgeUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "index":
		return cmdKnowledgeIndex(args[1:])
	case "status":
		return cmdKnowledgeStatus(args[1:])
	case "ls":
		return cmdKnowledgeLs(args[1:])
	case "help", "-h", "--help":
		knowledgeUsage(os.Stdout)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown knowledge command: %s\n", args[0])
	knowledgeUsage(os.Stderr)
	return 2
}

func knowledgeUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  nocturn knowledge index [-w workspace]   Bring the index in line with the folder
  nocturn knowledge status [-w workspace]  How much is indexed, and where
  nocturn knowledge ls [-w workspace]      The documents currently in the index

Documents live in <workspace>/mnt/knowledge/. Indexing embeds them, which SENDS THEM to the
configured embedding provider — see NOCTURN_EMBED_* in .env.example. Only changed files are
re-embedded, so running this twice costs nothing the second time.

A running daemon does the same thing every minute on its own.
`)
}

// openKnowledge opens one workspace and returns it with its document store, or explains why there is
// none. The workspace comes back too because it owns the store's lifetime — the caller closes it.
func openKnowledge(ws string) (*workspace.Workspace, *knowledge.Store, int) {
	if ws == "" {
		ws = "main"
	}
	w, err := openWorkspace(ws, newLogger(os.Stderr, slog.LevelWarn))
	if err != nil {
		fmt.Fprintln(os.Stderr, "knowledge:", err)
		return nil, nil, 1
	}
	k := w.Knowledge()
	if k == nil {
		w.Close()
		fmt.Fprintln(os.Stderr,
			"knowledge: no embedding endpoint configured — set NOCTURN_EMBED_BASE_URL (or OPENAI_BASE_URL)")
		return nil, nil, 1
	}
	return w, k, 0
}

func cmdKnowledgeIndex(args []string) int {
	fs := flag.NewFlagSet("knowledge index", flag.ContinueOnError)
	ws := fs.String("w", "main", "workspace")
	fs.StringVar(ws, "workspace", "main", "workspace")
	fs.Usage = usage(fs, "knowledge index [-w workspace]", "Bring the index in line with the folder.")
	if code, done := parseFlags(fs, args); done {
		return code
	}

	w, k, code := openKnowledge(*ws)
	if k == nil {
		return code
	}
	defer w.Close()

	fmt.Printf("indexing %s\n", k.Dir())
	started := time.Now()
	rep, err := k.Index(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "knowledge:", err)
		return 1
	}

	fmt.Printf("  %d indexed, %d unchanged, %d removed — %d passages in %s\n",
		rep.Indexed, rep.Unchanged, rep.Removed, rep.Chunks, time.Since(started).Round(time.Millisecond))
	// Named rather than counted: knowing that three files were skipped is not knowing which document
	// is silently not searchable.
	for _, p := range rep.Skipped {
		fmt.Printf("  skipped %s — no reader handles this format\n", p)
	}
	return 0
}

func cmdKnowledgeStatus(args []string) int {
	fs := flag.NewFlagSet("knowledge status", flag.ContinueOnError)
	ws := fs.String("w", "main", "workspace")
	fs.StringVar(ws, "workspace", "main", "workspace")
	fs.Usage = usage(fs, "knowledge status [-w workspace]", "How much is indexed, and where.")
	if code, done := parseFlags(fs, args); done {
		return code
	}

	w, k, code := openKnowledge(*ws)
	if k == nil {
		return code
	}
	defer w.Close()

	files, chunks, err := k.Stats()
	if err != nil {
		fmt.Fprintln(os.Stderr, "knowledge:", err)
		return 1
	}
	fmt.Printf("documents  %s\n", k.Dir())
	fmt.Printf("index      %s\n", k.IndexPath())
	fmt.Printf("indexed    %d file(s), %d passage(s)\n", files, chunks)
	if files == 0 {
		fmt.Println("\nnothing indexed yet — nocturn knowledge index")
	}
	return 0
}

func cmdKnowledgeLs(args []string) int {
	fs := flag.NewFlagSet("knowledge ls", flag.ContinueOnError)
	ws := fs.String("w", "main", "workspace")
	fs.StringVar(ws, "workspace", "main", "workspace")
	fs.Usage = usage(fs, "knowledge ls [-w workspace]", "The documents currently in the index.")
	if code, done := parseFlags(fs, args); done {
		return code
	}

	w, k, code := openKnowledge(*ws)
	if k == nil {
		return code
	}
	defer w.Close()

	paths, err := k.Paths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "knowledge:", err)
		return 1
	}
	if len(paths) == 0 {
		fmt.Println("nothing indexed — nocturn knowledge index")
		return 0
	}
	for _, p := range paths {
		fmt.Println(p)
	}
	return 0
}
