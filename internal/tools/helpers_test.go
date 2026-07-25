package tools_test

import (
	"context"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// toolFrom builds the base toolset for cfg and returns the named tool, failing the test when it is
// absent. It is the general form of the file/net-specific helpers already in this package.
func toolFrom(t *testing.T, cfg tools.Config, name string) agentkit.Tool {
	t.Helper()
	ts, err := tools.Base(cfg)
	if err != nil {
		t.Fatalf("Base: %v", err)
	}
	for _, tl := range ts {
		if tl.Spec().Name == name {
			return tl
		}
	}
	t.Fatalf("tool %q not found in Base", name)
	return nil
}

// allowAll installs an allow-everything policy so a Check reaches the tool's own effect path (egress
// scan, injection, socket, …) instead of stopping at the gate.
func allowAll(ctx context.Context) context.Context {
	return gate.With(ctx, gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.Allowed() }), nil, nil)
}

// denyAll installs a deny-everything policy — every gated action returns gate.ErrDenied before its
// effect runs.
func denyAll(ctx context.Context) context.Context {
	return gate.With(ctx, gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.Denied() }), nil, nil)
}

// capturePolicy installs a policy that records every Action it decides and applies fn to rule on it.
// The returned pointer's Actions is read only AFTER the tool call returns (single goroutine), so no
// lock is needed.
func capturePolicy(ctx context.Context, seen *[]gate.Action, fn func(gate.Action) gate.Ruling) context.Context {
	return gate.With(ctx, gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		*seen = append(*seen, a)
		return fn(a)
	}), nil, nil)
}

// fakeNotifier is a Notifier that records every delivery and can be told to fail. It is safe for the
// reminder/wake timer goroutines that call Notify off the test goroutine.
type fakeNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
	err   error
}

type notifyCall struct {
	title, message string
	ws, kind       string
	chatID         string
}

func (f *fakeNotifier) Notify(_ context.Context, n tools.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, notifyCall{
		title: n.Title, message: n.Message, ws: n.Ws, kind: n.Kind, chatID: n.ChatID,
	})
	return f.err
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeNotifier) last() (notifyCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return notifyCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

// fakeSessions is the wake tool's Sessions seam: it records every id Open is asked for and returns a
// pre-registered session (or nil, to model an unresolvable/reaped chat).
type fakeSessions struct {
	mu      sync.Mutex
	opened  []string
	session *agentkit.Session
}

func (f *fakeSessions) Open(id string) *agentkit.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, id)
	return f.session
}

func (f *fakeSessions) openedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opened...)
}

// recordLLM is a minimal agentkit.LLM that records the last user-message content it was asked to
// answer and always returns a final answer — enough to observe that a session actually ran a turn
// (its Submit reached the loop) without pulling in a real model.
type recordLLM struct {
	mu   sync.Mutex
	seen chan string
}

func newRecordLLM() *recordLLM { return &recordLLM{seen: make(chan string, 4)} }

func (l *recordLLM) Next(_ context.Context, conv []agentkit.Message, _ []agentkit.ToolSpec) (agentkit.Step, error) {
	var last string
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == agentkit.RoleUser {
			last = conv[i].Content
			break
		}
	}
	select {
	case l.seen <- last:
	default:
	}
	return agentkit.Step{Answer: "done"}, nil
}
