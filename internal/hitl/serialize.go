package hitl

import "sync"

// serialNotifier serializes approval prompts. Because Notify blocks until the
// human answers, a mutex around it guarantees at most one prompt is presented at
// a time: tool calls may run concurrently, but their approvals queue and the
// human reviews one at a time — deliberate single decisions, not a flood. It is
// transport-agnostic (wraps any Notifier: TUI or ntfy). Auto-allowed effects
// never reach a Notifier (they short-circuit in the gateway), so they are
// unaffected and stay fully parallel.
type serialNotifier struct {
	inner Notifier
	mu    sync.Mutex
}

// Serialize wraps n so its Notify calls never overlap. It is idempotent (a
// serialized notifier is returned unchanged) and passes nil through.
func Serialize(n Notifier) Notifier {
	if n == nil {
		return nil
	}
	if _, ok := n.(*serialNotifier); ok {
		return n
	}
	return &serialNotifier{inner: n}
}

func (s *serialNotifier) Notify(intent string, options []Option) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Notify(intent, options)
}
