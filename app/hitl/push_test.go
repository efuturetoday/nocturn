package hitl_test

import (
	"context"
	"testing"

	"github.com/efuturetoday/nocturn/app/hitl"
)

// TestLogPusher_Push_NeverDelivers_ReturnsNil: the placeholder Pusher reserves the out-of-band path
// but delivers nothing and never errors.
func TestLogPusher_Push_NeverDelivers_ReturnsNil(t *testing.T) {
	p := hitl.NewLogPusher(discard())
	if err := p.Push(context.Background(), "net → api.example.com"); err != nil {
		t.Errorf("Push err = %v, want nil", err)
	}
}
