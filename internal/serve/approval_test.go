package serve

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// fakeSink captures the id the broker presents so a test can resolve that exact approval.
type fakeSink struct{ gotID chan string }

func (s *fakeSink) Approval(_ context.Context, id string, _ uint64, _, _ string, _ []string) {
	select {
	case s.gotID <- id:
	default:
	}
}
func (s *fakeSink) Resolved(context.Context, string) {}

// approval.resolve forwards the chosen index to the broker: choice >= 0 approves the matching grant,
// -1 denies. Driven end-to-end through a real broker so the wire handler's Resolve call is observed
// by an Ask returning.
func TestApprovalResolve_ForwardsChoiceToBroker(t *testing.T) {
	tests := []struct {
		name       string
		choice     int
		wantApprov bool
	}{
		{"allow once", 0, true},
		{"deny with -1", -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broker := hitl.NewBroker(nil, slog.New(slog.DiscardHandler))
			sink := &fakeSink{gotID: make(chan string, 1)}
			ctx := context.Background()
			broker.Attach(ctx, sink) // active foreground sink so Ask presents to it

			c := testConn()
			c.broker = broker

			result := make(chan bool, 1)
			go func() {
				approved, _, _, _ := broker.Ask(ctx, gate.Action{Kind: "net", Target: "example.com"}, nil)
				result <- approved
			}()

			var id string
			select {
			case id = <-sink.gotID:
			case <-time.After(time.Second):
				t.Fatal("broker never presented the approval to the sink")
			}

			data, _ := json.Marshal(ApprovalResolve{Cmd: "approval.resolve", ID: id, Choice: tt.choice})
			c.approval(ctx, "approval.resolve", data)

			select {
			case got := <-result:
				if got != tt.wantApprov {
					t.Errorf("approved = %v, want %v", got, tt.wantApprov)
				}
			case <-time.After(time.Second):
				t.Fatal("Ask did not return — the resolve was not forwarded to the broker")
			}
		})
	}
}

func TestApprovalDispatch_BadJSON_Error(t *testing.T) {
	c := testConn()
	c.broker = hitl.NewBroker(nil, slog.New(slog.DiscardHandler))
	c.approval(context.Background(), "approval.resolve", []byte(`{bad`))
	recvError(t, c, "bad approval.resolve")
}

func TestApproval_UnknownAction_Error(t *testing.T) {
	c := testConn()
	c.approval(context.Background(), "approval.bogus", []byte(`{"cmd":"approval.bogus"}`))
	recvError(t, c, "unknown action")
}

// conn.Approval (hitl.Sink) forwards a pending approval to the client, preserving the tool-call frame
// and the raising chat id (provenance).
func TestConn_Approval_SendsRequestWithFrame(t *testing.T) {
	c := testConn()
	c.Approval(context.Background(), "id1", 7, "chat42", "net → example.com", []string{"allow", "deny"})

	req, ok := recv(t, c).(ApprovalRequest)
	if !ok {
		t.Fatalf("want ApprovalRequest")
	}
	if req.Type != "approval.request" || req.ID != "id1" || req.Frame != 7 || req.ChatID != "chat42" {
		t.Errorf("got %+v", req)
	}
	if req.Intent != "net → example.com" || len(req.Options) != 2 {
		t.Errorf("got %+v", req)
	}
}

func TestConn_Resolved_SendsResolved(t *testing.T) {
	c := testConn()
	c.Resolved(context.Background(), "id1")
	res, ok := recv(t, c).(ApprovalResolved)
	if !ok {
		t.Fatalf("want ApprovalResolved")
	}
	if res.Type != "approval.resolved" || res.ID != "id1" {
		t.Errorf("got %+v", res)
	}
}

// Frame 0 means "not tool-scoped" and is omitted from the wire (omitempty); a non-zero frame is kept.
func TestApprovalRequest_Frame0_Omitted(t *testing.T) {
	b, _ := json.Marshal(ApprovalRequest{Type: "approval.request", ID: "x", Frame: 0, Intent: "i"})
	if contains(string(b), `"frame"`) {
		t.Errorf("frame 0 must be omitted, wire = %s", b)
	}
	b, _ = json.Marshal(ApprovalRequest{Type: "approval.request", ID: "x", Frame: 5, Intent: "i"})
	if !contains(string(b), `"frame":5`) {
		t.Errorf("non-zero frame must be present, wire = %s", b)
	}
}
