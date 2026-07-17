package agent_test

import (
	"context"
	"errors"
	"strings"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// scriptedModel returns a fixed sequence of steps and records every conversation it
// was shown, so a test can assert what the model saw (e.g. a fed-back tool result).
type scriptedModel struct {
	steps []brain.Step
	calls int
	convs [][]brain.Message
}

func (m *scriptedModel) Next(_ context.Context, conv []brain.Message, _ []tool.Spec) (brain.Step, error) {
	m.convs = append(m.convs, append([]brain.Message(nil), conv...))
	s := m.steps[m.calls]
	m.calls++
	return s, nil
}

func convContains(conv []brain.Message, substr string) bool {
	for _, m := range conv {
		if strings.Contains(m.Content, substr) {
			return true
		}
	}
	return false
}

// autoNotifier resolves the pending approval immediately, picking the option that
// matches its desired outcome, and counts how often the human was asked.
type autoNotifier struct {
	want    hitl.Outcome
	resolve func(token string) error
	calls   int
}

func (n *autoNotifier) Notify(_ string, options []hitl.Option) error {
	n.calls++
	for _, o := range options {
		if o.Outcome == n.want {
			return n.resolve(o.Token)
		}
	}
	return errors.New("autoNotifier: no matching option")
}
