package tui

// These tests drive the components' key handling directly — build the component, ask it for its
// KeyMap, fire a binding, check what happened. That is the level the framework's own testing guide
// describes, and the only level the assembled UI is testable at: NewApp and NewAppWithReader both
// construct an ANSITerminal on os.Stdout and enter raw mode, so a MockTerminal cannot be handed to
// an App even though the type exists.

import (
	"strings"
	"testing"

	gotui "github.com/grindlemire/go-tui"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/tui/logring"
)

// newTestApp builds the root with no workspace. Every path exercised here stays out of the
// workspace; a test that needs one would have to open a real one.
func newTestApp(t *testing.T) *app {
	t.Helper()
	return newApp(t.Context(), Deps{
		Feed:     NewFeed(),
		Approver: NewApprover(),
		Ring:     logring.New(16),
		Model:    "test-model",
	}, make(chan opened, 1))
}

// press finds the binding matching a rune (with its modifiers) and fires it. It fails the test if
// no binding matches — a key that is not offered is exactly the bug this file exists for.
// An exact binding first, then a catch-all — the order the dispatch table resolves them in. Without
// the second pass a table whose typing binding is AnyRune (the palette's filter, the workspace
// view's) would look like a table that takes no letters at all.
func press(t *testing.T, km gotui.KeyMap, r rune) {
	t.Helper()
	for _, b := range km {
		if b.Pattern.Rune == r && b.Pattern.Mod == 0 {
			b.Handler(gotui.KeyEvent{Key: gotui.KeyRune, Rune: r})
			return
		}
	}
	for _, b := range km {
		if b.Pattern.AnyRune && b.Pattern.Mod == 0 {
			b.Handler(gotui.KeyEvent{Key: gotui.KeyRune, Rune: r})
			return
		}
	}
	t.Fatalf("no binding for %q in the current KeyMap", string(r))
}

func pressKey(t *testing.T, km gotui.KeyMap, k gotui.Key) {
	t.Helper()
	for _, b := range km {
		if b.Pattern.Key == k {
			b.Handler(gotui.KeyEvent{Key: k})
			return
		}
	}
	t.Fatalf("no binding for key %v in the current KeyMap", k)
}

// answered runs one Ask and reports the decision it came back with.
type answered struct {
	approved bool
	recall   gate.Recall
	target   string
}

func pendingAsk(t *testing.T, a *app) (*Ask, <-chan answered) {
	t.Helper()
	out := make(chan answered, 1)
	go func() {
		ok, grant, recall, _ := a.approver.Ask(t.Context(),
			gate.Action{Kind: "net", Target: "api.example.com"},
			[]gate.Grant{{Kind: "net", Target: "*.example.com"}})
		out <- answered{ok, recall, grant.Target}
	}()
	ask := <-a.approver.Asks()
	a.onAsk(ask)
	return ask, out
}

// The reported bug: with the bindings on the modal, the cached instance kept the keymap it was
// built with and the digits did nothing. They belong to the root, which rebuilds per render.
func TestApprovalDigitAnswersTheOpenAsk(t *testing.T) {
	a := newTestApp(t)
	_, res := pendingAsk(t, a)

	press(t, a.KeyMap(), '2') // "this session"

	got := <-res
	if !got.approved || got.recall != gate.RecallSession {
		t.Fatalf("Ask() = approved %v, recall %v, want an approval for the session", got.approved, got.recall)
	}
	if a.asking.Get() || a.ask.Get() != nil {
		t.Error("the modal is still open after it was answered")
	}
}

func TestApprovalWideningOptionIsReachable(t *testing.T) {
	a := newTestApp(t)
	_, res := pendingAsk(t, a)

	press(t, a.KeyMap(), '4') // once / session / always / always *.example.com

	if got := <-res; got.target != "*.example.com" {
		t.Errorf("granted target = %q, want the widening suggestion", got.target)
	}
}

func TestEscapeDeniesTheOpenAsk(t *testing.T) {
	a := newTestApp(t)
	_, res := pendingAsk(t, a)

	pressKey(t, a.KeyMap(), gotui.KeyEscape)

	if got := <-res; got.approved {
		t.Error("Escape approved the action, want a refusal")
	}
	if a.asking.Get() {
		t.Error("the modal stayed open after Escape")
	}
}

// Ctrl+C during an approval denies it rather than cancelling the turn: the ask IS what the turn is
// waiting on, so answering it is the way to let the turn move.
func TestCtrlCDeniesWhileAnApprovalIsOpen(t *testing.T) {
	a := newTestApp(t)
	_, res := pendingAsk(t, a)

	a.cancel()

	if got := <-res; got.approved {
		t.Error("Ctrl+C approved the action, want a refusal")
	}
}

// The option keys must preempt. A trapping modal ends its KeyMap with OnPreemptStop(AnyKey), and the
// preempt pass is the only pass that runs before it; an ordinary binding would never be reached.
func TestApprovalKeysPreempt(t *testing.T) {
	a := newTestApp(t)
	ask, res := pendingAsk(t, a)
	defer func() { ask.Deny(); <-res }()

	for _, want := range []rune{'1', '2', 'n'} {
		b, ok := bindingFor(a.KeyMap(), want, 0)
		if !ok {
			t.Fatalf("no binding for %q while an approval is open", string(want))
		}
		if !b.Preempt || !b.Stop {
			t.Errorf("binding for %q: preempt=%v stop=%v, want both — the modal's catch-all runs after the preempt pass",
				string(want), b.Preempt, b.Stop)
		}
	}
}

// Quitting has to survive the modal's catch-all too: a turn blocked on an approval must not be able
// to trap the person in the UI.
func TestQuitAndCancelAlwaysPreempt(t *testing.T) {
	a := newTestApp(t)

	for _, want := range []rune{'q', 'c'} {
		b, ok := bindingFor(a.KeyMap(), want, gotui.ModCtrl)
		if !ok {
			t.Fatalf("no binding for Ctrl+%q", string(want))
		}
		if !b.Preempt {
			t.Errorf("Ctrl+%q does not preempt, so an open approval would swallow it", string(want))
		}
	}
}

// The whole of bug 2 in one assertion. Every root binding must carry a modifier: the composer is a
// real text field, and a bare letter on the root would be typed into it or stolen from it depending
// on which of the two the dispatch table reached first. Pane-local letters live on the panes, gated
// on focus.
func TestRootClaimsNoBareLetters(t *testing.T) {
	a := newTestApp(t)

	for _, b := range a.KeyMap() {
		if b.Pattern.Rune != 0 && b.Pattern.Mod == 0 {
			t.Errorf("the root binds the bare rune %q; it would fight the composer for it", string(b.Pattern.Rune))
		}
		if b.Pattern.AnyRune {
			t.Error("the root binds AnyRune; the composer owns typing")
		}
	}
}

func bindingFor(km gotui.KeyMap, r rune, mod gotui.Modifier) (gotui.KeyBinding, bool) {
	for _, b := range km {
		if b.Pattern.Rune == r && b.Pattern.Mod == mod {
			return b, true
		}
	}
	return gotui.KeyBinding{}, false
}

// A label longer than the column is ellipsized, never wrapped: the sidebar is one row per chat and
// the cursor walks row indices, so a wrapped name would put every row below it out of step.
func TestSidebarLabelsFitTheColumn(t *testing.T) {
	long := strings.Repeat("ä", 80) // umlauts: a byte-counting truncation would cut these in half
	got := oneLine(long, labelWidth)

	if n := len([]rune(got)); n != labelWidth {
		t.Errorf("label is %d runes, want %d", n, labelWidth)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("label = %q, want it to end in an ellipsis", got)
	}
	if strings.Contains(got, "�") {
		t.Error("the truncation split a rune")
	}
}

func TestShortLabelsAreLeftAlone(t *testing.T) {
	if got := oneLine("Hallo", labelWidth); got != "Hallo" {
		t.Errorf("oneLine = %q, want the name untouched", got)
	}
}
