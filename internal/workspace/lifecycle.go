package workspace

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/knowledge"
)

// This file is the workspace's LIFETIME: what it starts in the background, and what stops all of it.
//
// It is its own file because the two ends belong together and to nothing else. Close used to sit
// among the agent code, which is where it grew its blind spot: it stopped the chat managers and left
// the reminder timers, the wake timers and the document reconcile running. That was invisible while
// the only caller was daemon shutdown, and became a leak per deleted workspace the moment a workspace
// could be removed while the process ran on.

// Close stops everything this workspace runs in the background: the work StartAgents owns (the cron
// scheduler and the document reconcile), the reminder and wake timers, both chat managers (their
// reapers and every live session), and any spoken session.
//
// It must stop ALL of them, and that is worth saying because it used to stop only the last two.
// Closing a workspace was a shutdown-only concern then, so the daemon exiting hid what was left
// running. It stops being hidden the moment a workspace can be deleted while the process lives on: a
// closed workspace whose reminder timers still fire delivers notifications through a notifier nobody
// observes any more, and its Watch goroutine keeps reconciling a directory that is on its way to the
// trash. Idempotent — every step below tolerates being called twice.
func (w *Workspace) Close() {
	w.stopBackground()
	w.reminders.Cancel()
	w.waker.Cancel()
	w.chats.CloseAll()
	w.agentChats.CloseAll()
	w.closePlugins()
	if w.mailbox != nil {
		// After the chat managers, like the plugins and for the same reason: nothing can still be in
		// a turn that would open a fresh session on a workspace on its way out.
		w.mailbox.Close()
	}
	if w.voice != nil {
		w.voice.CloseAll()
	}
}

// closePlugins releases every wasm guest this workspace ever compiled — the current snapshot's and
// every superseded one's. It runs after the chat managers are closed, so nothing can still be calling
// into one.
func (w *Workspace) closePlugins() {
	w.retiredMu.Lock()
	all := w.retired
	w.retired = nil
	w.retiredMu.Unlock()
	if a := w.snapshot(); a != nil {
		all = append(all, a.plugins...)
	}
	for _, p := range all {
		if err := p.Close(context.Background()); err != nil {
			w.log.Warn("closing plugin failed", "plugin", p.Name(), "err", err)
		}
	}
}

// stopBackground cancels what StartAgents started and latches the workspace closed, so a StartAgents
// that has not reached its registration yet finds the door shut rather than starting work nobody can
// stop. The latch is the whole point: the daemon starts the schedulers in goroutines, so "closed
// before started" is an ordinary interleaving, not a corner case.
func (w *Workspace) stopBackground() {
	w.bgMu.Lock()
	w.bgClosed = true
	stop := w.stopBg
	w.stopBg = nil
	w.bgMu.Unlock()
	if stop != nil {
		stop()
	}
}

// StartAgents runs the cron scheduler until ctx is cancelled or the workspace is closed — call it in
// a goroutine.
//
// The document reconcile rides along, because both are the same kind of thing: background work a
// workspace does while nobody is asking it anything. Starting it here means one call site keeps
// both, rather than a second one somebody adds to the daemon and forgets in the terminal.
//
// The derived context is what lets Close stop them without a second lifetime owner. Only the cancel
// func is kept, never the context: a context belongs in an argument, a shutdown handle is a handle.
// Starting an already-closed workspace is a no-op rather than a race — see stopBackground.
func (w *Workspace) StartAgents(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w.bgMu.Lock()
	closed := w.bgClosed
	if !closed {
		w.stopBg = cancel
	}
	w.bgMu.Unlock()
	if closed {
		return
	}

	if w.knowledge != nil {
		// Its own goroutine: a reconcile that is waiting on an embedding provider must not hold up a
		// scheduled agent, and vice versa.
		go w.knowledge.Watch(ctx, knowledge.DefaultInterval)
	}
	w.runScheduler(ctx)
}

// runScheduler keeps a cron scheduler running over the CURRENT agent set, replacing it whenever a
// reload changes what that set is.
//
// A Scheduler captures its agents when it is built and cannot be re-aimed, so a reload has to build a
// new one. It is done as a generation loop rather than by storing a context somewhere: each pass
// derives a child context, runs a scheduler on it, and waits for either the caller's context to end
// or a reload to signal. That keeps the context flowing downward through arguments, which is where a
// context belongs, and keeps one owner for this lifetime instead of two.
//
// Exactly one generation runs at a time. Cancelling is not the same as having stopped — a scheduler
// notices its context at the next select — so the loop WAITS for the old one to return before
// building the next. Starting the replacement immediately would leave two schedulers overlapping for
// a moment, both holding an agent that is due, and a cron agent firing twice is not a race you would
// ever catch from the outside: it just did its work twice.
func (w *Workspace) runScheduler(ctx context.Context) {
	for {
		gen, cancel := context.WithCancel(ctx)
		// Take the signal BEFORE reading the snapshot, and that order is the whole correctness of the
		// loop. This way a reload landing in between leaves us holding an already-closed channel: one
		// wasted iteration, then the new agent set. The other order would read the old set and then
		// wait on the signal that reload already fired — missing it, and scheduling the old agents
		// until the next reload happened to come along.
		reloaded := w.reloadSignal()
		sched := agent.NewScheduler(w.snapshot().agents, w.log, func(ctx context.Context, a agent.Agent) {
			// A scheduled firing is fire-and-forget; the run streams + persists like any chat. Surface
			// only a start-time rejection (unknown agent / shutting down) — the run's own errors land in
			// its transcript.
			if _, err := w.FireAgent(ctx, a.Name, "Run your scheduled task now."); err != nil {
				w.log.With("component", "scheduler").Error("scheduled agent failed", "agent", a.Name, "err", err)
			}
		})
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			sched.Start(gen)
		}()

		select {
		case <-ctx.Done():
			cancel()
			<-stopped
			return
		case <-reloaded:
			cancel()
			<-stopped
		}
	}
}

// reloadSignal returns the channel closed by the next reload.
func (w *Workspace) reloadSignal() <-chan struct{} {
	w.bgMu.Lock()
	defer w.bgMu.Unlock()
	return w.reloaded
}

// restartScheduler tells a running scheduler generation to stand down so the next one picks up the
// new agent set. Closing the channel and replacing it is what lets a waiter with the old one still
// see it fire — a plain "reload happened" broadcast, with no state to get out of step.
func (w *Workspace) restartScheduler() {
	w.bgMu.Lock()
	prev := w.reloaded
	w.reloaded = make(chan struct{})
	w.bgMu.Unlock()
	close(prev)
}
