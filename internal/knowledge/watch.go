package knowledge

import (
	"context"
	"time"
)

// DefaultInterval is how often a running daemon reconciles the corpus with the index.
//
// A minute rather than a second because of what a reconcile now costs: an unchanged corpus is a
// directory walk, no file is opened, and nothing is written. Dropping a document in the folder and
// waiting up to a minute for it to be findable is the right trade against waking the machine sixty
// times as often to learn nothing.
const DefaultInterval = time.Minute

// Watch reconciles the corpus with the index until ctx is done.
//
// A ticker rather than filesystem notifications, and that is a decision rather than a shortcut.
// Watching would mean a third-party dependency in a tree that keeps its list deliberately short,
// recursive watch management for every subdirectory that appears, and platform behaviour that
// silently drops events under load — after which a periodic reconcile is needed anyway as the
// backstop. This is only the backstop, without the part that would still need it.
//
// It runs once immediately, so a daemon starting up does not wait an interval before noticing what
// changed while it was down. Every run is logged: a reconcile that quietly costs money is exactly
// what an operator should be able to see.
func (s *Store) Watch(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = DefaultInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()

	for {
		s.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// reconcile is one pass, with its outcome logged and its failure survived.
//
// A failing run must not end the watcher: the usual cause is the embedding provider being briefly
// unreachable, and a daemon that stopped watching after one timeout would stay stale until somebody
// restarted it — with nothing saying so.
func (s *Store) reconcile(ctx context.Context) {
	rep, err := s.Index(ctx)
	switch {
	case ctx.Err() != nil:
		return // shutting down; not worth a line
	case err != nil:
		s.log.Warn("knowledge: reconcile failed", "err", err)
	case rep.Indexed > 0 || rep.Removed > 0:
		s.log.Info("knowledge: index updated",
			"indexed", rep.Indexed, "removed", rep.Removed, "unchanged", rep.Unchanged, "chunks", rep.Chunks)
	default:
		s.log.Debug("knowledge: nothing changed", "files", rep.Unchanged, "chunks", rep.Chunks)
	}
	// Named at most once per run, and only when there is something to name: a folder holding a PDF
	// would otherwise say so every minute forever.
	if len(rep.Skipped) > 0 {
		s.log.Debug("knowledge: files no reader handles", "paths", rep.Skipped)
	}
}
