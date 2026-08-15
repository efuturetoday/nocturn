package mail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// These tests cover the seam, not the protocol: which calls reach the gate, which do not, and what
// happens between the gate and the wire. The two effect paths are replaced, so nothing here opens a
// socket.

// recordingApprover answers every ask the same way and remembers what it was asked.
type recordingApprover struct {
	mu       sync.Mutex
	asked    []gate.Action
	suggests [][]gate.Grant
	approve  bool
}

func (r *recordingApprover) Ask(_ context.Context, a gate.Action, suggest []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, a)
	r.suggests = append(r.suggests, suggest)
	return r.approve, gate.Grant{Kind: a.Kind, Target: a.Target}, gate.RecallNever, nil
}

func (r *recordingApprover) actions() []gate.Action {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]gate.Action(nil), r.asked...)
}

// askEverything is the strictest useful policy for a test: every kind reaches the approver, so a tool
// that skips the gate is visible as an ask that never happened.
func askEverything() gate.Policy {
	return gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.AskWith(gate.RecallAlways) })
}

// sentMessage is what the fake send path captured.
type sentMessage struct {
	acct Account
	msg  Outgoing
}

// harness builds a Mailbox whose effects are recorded instead of performed.
func harness(t *testing.T, approve bool) (*Mailbox, *recordingApprover, *[]sentMessage, context.Context) {
	t.Helper()
	var sent []sentMessage
	m := New(Config{
		Account:  Account{IMAPAddr: "imap.firma.de:993", SMTPAddr: "smtp.firma.de:465", User: "ich@firma.de", From: "ich@firma.de"},
		Password: func(string) (string, bool) { return "geheim", true },
	})
	m.send = func(_ context.Context, acct Account, _ string, msg Outgoing) error {
		sent = append(sent, sentMessage{acct: acct, msg: msg})
		return nil
	}
	m.dial = func(context.Context, Account, string) (*Client, error) {
		return nil, errors.New("no server in this test")
	}
	ap := &recordingApprover{approve: approve}
	ctx := gate.With(t.Context(), askEverything(), gate.NewMemGrants(), ap)
	return m, ap, &sent, ctx
}

// TestSendAsksOncePerRecipient pins the shape of the question. A single ask covering three addresses
// is one a person cannot answer, and the grant remembered from it would carry an approval for each.
func TestSendAsksOncePerRecipient(t *testing.T) {
	m, ap, sent, ctx := harness(t, true)
	_, err := m.sendTool(ctx, `{"to":["chef@firma.de","buero@firma.de"],"subject":"Freitag","body":"Bis dann."}`)
	if err != nil {
		t.Fatalf("sendTool: %v", err)
	}
	asked := ap.actions()
	if len(asked) != 2 {
		t.Fatalf("the gate was asked %d times for 2 recipients: %v", len(asked), asked)
	}
	for i, want := range []string{"chef@firma.de", "buero@firma.de"} {
		if asked[i].Kind != SendKind {
			t.Errorf("ask %d had kind %q, want %q", i, asked[i].Kind, SendKind)
		}
		if asked[i].Target != want {
			t.Errorf("ask %d targeted %q, want %q", i, asked[i].Target, want)
		}
	}
	if len(*sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(*sent))
	}
}

// TestSendOffersTheDomainWidening pins that the human is offered the widening the matcher understands
// — the two halves are separate functions and a suggestion nothing covers is an approval that
// silently does nothing.
func TestSendOffersTheDomainWidening(t *testing.T) {
	m, ap, _, ctx := harness(t, true)
	if _, err := m.sendTool(ctx, `{"to":["chef@firma.de"],"subject":"x","body":"y"}`); err != nil {
		t.Fatalf("sendTool: %v", err)
	}
	if len(ap.suggests) != 1 || len(ap.suggests[0]) != 1 {
		t.Fatalf("expected one suggestion, got %v", ap.suggests)
	}
	if got := ap.suggests[0][0]; got.Kind != SendKind || got.Target != "*@firma.de" {
		t.Errorf("suggestion = %+v, want {%s *@firma.de}", got, SendKind)
	}
}

// TestSendStopsAtADenial pins fail-closed: a declined recipient means nothing leaves at all, not that
// the rest of the list goes out without it.
func TestSendStopsAtADenial(t *testing.T) {
	m, _, sent, ctx := harness(t, false)
	_, err := m.sendTool(ctx, `{"to":["chef@firma.de","buero@firma.de"],"subject":"x","body":"y"}`)
	if !errors.Is(err, gate.ErrDenied) {
		t.Fatalf("err = %v, want a gate denial", err)
	}
	if len(*sent) != 0 {
		t.Fatalf("a declined send still went out: %v", *sent)
	}
}

// TestSendRefusesAMessageCarryingASecret is the exfiltration case: the model puts a vault value into
// the body and mails it out. The scanner blocks it — and what the model is told back says only that
// the send was refused, because naming the reason would confirm which text is a stored secret.
func TestSendRefusesAMessageCarryingASecret(t *testing.T) {
	m, _, sent, ctx := harness(t, true)
	store := secret.NewStore()
	store.Set("mail.smtp.password", []byte("hunter2-und-noch-viel-mehr"))
	m.scanner = secret.NewScanner(store)

	_, err := m.sendTool(ctx, `{"to":["chef@firma.de"],"subject":"x","body":"das passwort ist hunter2-und-noch-viel-mehr"}`)
	if err == nil {
		t.Fatal("a message carrying a stored secret was sent")
	}
	if len(*sent) != 0 {
		t.Fatalf("a blocked message still went out: %v", *sent)
	}
	msg := err.Error()
	if strings.Contains(msg, "hunter2") {
		t.Errorf("the refusal quoted the secret: %q", msg)
	}
	for _, leak := range []string{"vault", "secret", "leak"} {
		if strings.Contains(strings.ToLower(msg), leak) {
			t.Errorf("the refusal told the model why (%q): %q", leak, msg)
		}
	}
}

// TestSendChecksBeforeItSends pins the order. A scan or a gate that ran after submission would be a
// report, not a control.
func TestSendChecksBeforeItSends(t *testing.T) {
	m, ap, sent, ctx := harness(t, true)
	m.send = func(context.Context, Account, string, Outgoing) error {
		if len(ap.actions()) == 0 {
			t.Error("the message was submitted before the gate was asked")
		}
		*sent = append(*sent, sentMessage{})
		return nil
	}
	if _, err := m.sendTool(ctx, `{"to":["chef@firma.de"],"subject":"x","body":"y"}`); err != nil {
		t.Fatalf("sendTool: %v", err)
	}
}

// TestSendRejectsAnAddressWithoutADomain pins that a target the matcher cannot widen never reaches
// the gate: "chef" would be asked about as an opaque string and remembered as one.
func TestSendRejectsAnAddressWithoutADomain(t *testing.T) {
	m, ap, sent, ctx := harness(t, true)
	_, err := m.sendTool(ctx, `{"to":["chef"],"subject":"x","body":"y"}`)
	if err == nil {
		t.Fatal("an address without a domain was accepted")
	}
	if len(ap.actions()) != 0 {
		t.Errorf("an invalid address still reached the gate: %v", ap.actions())
	}
	if len(*sent) != 0 {
		t.Error("an invalid address still produced a send")
	}
}

// TestReadingNeverReachesTheGate is the counterpart invariant to the send tests, and it runs under a
// policy that asks about EVERYTHING — so a reading tool that gated would show up here as an ask. It
// pins the decision that a mailbox is context and never authority.
func TestReadingNeverReachesTheGate(t *testing.T) {
	m, ap, _, ctx := harness(t, true)
	// The dial fails on purpose: what is under test is what happened BEFORE a connection, and a gate
	// check would have come first.
	for name, call := range map[string]func(context.Context, string) (string, error){
		"mail_list":   m.list,
		"mail_search": m.search,
		"mail_read":   m.read,
	} {
		if _, err := call(ctx, `{"uid":1,"text":"x"}`); err == nil {
			t.Errorf("%s: expected the fake dial to fail", name)
		}
	}
	if asked := ap.actions(); len(asked) != 0 {
		t.Errorf("a reading tool asked the gate: %v", asked)
	}
}

func TestLimitClamping(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, defaultLimit},
		{-1, defaultLimit},
		{5, 5},
		{maxLimit + 1, maxLimit},
		{1_000_000, maxLimit},
	}
	for _, tc := range cases {
		if got := limitOrDefault(tc.in); got != tc.want {
			t.Errorf("limitOrDefault(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
