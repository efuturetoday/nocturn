package workspace

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/app/agent"
	"github.com/efuturetoday/nocturn/app/chat"
	"github.com/efuturetoday/nocturn/app/tools"
)

// Agents returns the workspace's declared agents, sorted by name.
func (w *Workspace) Agents() []agent.Agent { return w.agents.All() }

// StartAgents runs the cron scheduler until ctx is cancelled — call it in a goroutine.
func (w *Workspace) StartAgents(ctx context.Context) { w.sched.Start(ctx) }

// AgentRuns lists the persisted agent-run transcripts, most recent first.
func (w *Workspace) AgentRuns() ([]chat.Meta, error) { return w.agentStore.Metas() }

// FireAgent runs the named agent once, unattended, over task; it persists the transcript to the
// agent store and returns the agent's final answer. Unattended = no approver: the agent may use
// standing durable grants, but any fresh Ask is denied (fail-closed). Its tool cage is the workspace
// toolset filtered to the agent's declared tools.
func (w *Workspace) FireAgent(ctx context.Context, name, task string) (string, error) {
	a, ok := w.agents.Get(name)
	if !ok {
		return "", fmt.Errorf("workspace %q: no agent %q", w.name, name)
	}

	runID := chat.NewID()
	log := w.log.With("component", "agent", "agent", name, "run", runID)
	log.Info("agent run started", "unattended", true)
	start := time.Now()

	rt := runtime.New(w.llm,
		runtime.WithTools(w.tools.Select(a.Matches)),
		runtime.WithGate(policy(), w.grants, nil),          // nil approver = unattended
		runtime.WithGateLogger(agentkit.SlogLogger(w.log)), // trace the unattended fail-closed denials
		runtime.WithSession(
			agentkit.WithSystem(a.Instructions),
			agentkit.WithEffort(a.Effort),
			agentkit.WithTimeout(turnTimeout),
			agentkit.WithLogger(agentkit.SlogLogger(w.log)),
		),
	)

	// Stamp the run id as the chat id: an agent run IS an openable transcript, so a notify or a
	// reminder it sets carries provenance back to it exactly like one from a user chat.
	ctx = tools.WithChatID(ctx, runID)

	sess := rt.Session(ctx, agentkit.WithStore(w.agentStore, runID))
	defer sess.Close()

	var answer strings.Builder
	done := make(chan error, 1)
	go func() {
		for ev := range sess.Subscribe() {
			switch e := ev.(type) {
			case agentkit.Token:
				if e.Frame == 0 {
					answer.WriteString(e.Text)
				}
			case agentkit.TurnEnd:
				done <- e.Err
				return
			}
		}
		done <- nil
	}()
	sess.Submit(task)
	select {
	case err := <-done:
		log.Info("agent run finished", "dur", time.Since(start).Round(time.Millisecond), "answer_len", answer.Len(), "err", err)
		return answer.String(), err
	case <-ctx.Done():
		// Stop the stream and wait for the goroutine to stop writing answer before
		// we read it — strings.Builder is not concurrency-safe.
		sess.Close()
		<-done
		log.Warn("agent run interrupted", "dur", time.Since(start).Round(time.Millisecond), "err", ctx.Err())
		return answer.String(), ctx.Err()
	}
}
