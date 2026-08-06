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

func (s *fakeSink) Approval(_ context.Context, a hitl.Approval) {
	select {
	case s.gotID <- a.ID:
	default:
	}
}
func (s *fakeSink) Resolved(context.Context, string) {}

// approval.resolve forwards the chosen option id to the broker: an offered id approves the matching
// grant, the reserved deny id and anything never offered refuse. The empty string is the case a
// truncated or older message produces, and it must not approve. Driven end-to-end through a real
// broker so the wire handler's Resolve call is observed by an Ask returning.
func TestApprovalResolve_ForwardsOptionToBroker(t *testing.T) {
	tests := []struct {
		name       string
		option     string
		wantApprov bool
	}{
		{"allow once", "once", true},
		{"explicit deny", hitl.DenyOption, false},
		{"omitted option", "", false},
		{"never offered", "widen0", false},
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

			data, _ := json.Marshal(ApprovalResolve{Cmd: "approval.resolve", ID: id, Option: tt.option})
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

// conn.Approval (hitl.Sink) forwards a pending approval to the client as structure: the action's
// kind and target as separate fields, the tool-call frame and the raising chat id (provenance), and
// one option per answer with its recall — a widening carrying the broader grant it would create.
func TestConn_Approval_SendsStructuredRequest(t *testing.T) {
	c := testConn()
	c.Approval(context.Background(), hitl.Approval{
		ID:     "id1",
		Frame:  7,
		ChatID: "chat42",
		Action: gate.Action{Kind: "net", Target: "api.example.com"},
		Options: []hitl.Option{
			{ID: "once", Recall: gate.RecallNever, Grant: gate.Grant{Kind: "net", Target: "api.example.com"}},
			{ID: "widen0", Recall: gate.RecallAlways, Grant: gate.Grant{Kind: "net", Target: "*.example.com"}, Widens: true},
		},
	})

	req, ok := recv(t, c).(ApprovalRequest)
	if !ok {
		t.Fatalf("want ApprovalRequest")
	}
	if req.Type != "approval.request" || req.ID != "id1" || req.Frame != 7 || req.ChatID != "chat42" {
		t.Errorf("got %+v", req)
	}
	if req.Kind != "net" || req.Target != "api.example.com" {
		t.Errorf("action must reach the device as fields, got %+v", req)
	}
	if len(req.Options) != 2 {
		t.Fatalf("options = %+v, want 2", req.Options)
	}
	if req.Options[0].ID != "once" || req.Options[0].Recall != "never" || req.Options[0].Widen != nil {
		t.Errorf("exact option got %+v", req.Options[0])
	}
	w := req.Options[1]
	if w.ID != "widen0" || w.Recall != "always" || w.Widen == nil {
		t.Fatalf("widening option got %+v", w)
	}
	if w.Widen.Kind != "net" || w.Widen.Target != "*.example.com" {
		t.Errorf("widening grant got %+v", *w.Widen)
	}
}

// A widening is told apart from an exact grant by the PRESENCE of "widen" on the wire, so a plain
// option must omit the key entirely rather than send an empty object the device has to inspect.
func TestApprovalOption_WidenOmittedWhenExact(t *testing.T) {
	b, _ := json.Marshal(ApprovalOption{ID: "always", Recall: "always"})
	if contains(string(b), `"widen"`) {
		t.Errorf("an exact option must omit widen, wire = %s", b)
	}
	b, _ = json.Marshal(ApprovalOption{ID: "widen0", Recall: "always", Widen: &ApprovalGrant{Kind: "file", Target: "notes/*"}})
	if !contains(string(b), `"widen":{"kind":"file","target":"notes/*"}`) {
		t.Errorf("a widening must carry its grant, wire = %s", b)
	}
}

// recallName is the wire spelling of a recall, and an unrecognised value names the NARROWEST one —
// a device must never be shown a wider promise than the daemon would keep.
func TestRecallName_UnknownIsNarrowest(t *testing.T) {
	tests := []struct {
		in   gate.Recall
		want string
	}{
		{gate.RecallNever, "never"},
		{gate.RecallSession, "session"},
		{gate.RecallAlways, "always"},
		{gate.Recall(99), "never"},
	}
	for _, tt := range tests {
		if got := recallName(tt.in); got != tt.want {
			t.Errorf("recallName(%v) = %q, want %q", tt.in, got, tt.want)
		}
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
	b, _ := json.Marshal(ApprovalRequest{Type: "approval.request", ID: "x", Frame: 0, Kind: "net"})
	if contains(string(b), `"frame"`) {
		t.Errorf("frame 0 must be omitted, wire = %s", b)
	}
	b, _ = json.Marshal(ApprovalRequest{Type: "approval.request", ID: "x", Frame: 5, Kind: "net"})
	if !contains(string(b), `"frame":5`) {
		t.Errorf("non-zero frame must be present, wire = %s", b)
	}
}
