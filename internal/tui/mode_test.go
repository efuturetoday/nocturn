package tui

// The mode model is what the whole redesign rests on, so it is tested where it is decided rather
// than where it is drawn: mode() and region() are derivations, hintsFor is a pure function of them,
// and the key tables are values. None of it needs a terminal, which is the point.

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	gotui "github.com/grindlemire/go-tui"
)

// Every combination of the three flags, and the one thing that matters about them: exactly one mode
// comes out, and the one that comes out is the one whose question is most urgent.
func TestModeRanksTheOverlays(t *testing.T) {
	for _, tc := range []struct {
		asking, palette, inspect bool
		want                     mode
	}{
		{want: modeChat},
		{inspect: true, want: modeInspect},
		{palette: true, want: modePalette},
		{asking: true, want: modeApprove},
		// A turn blocked on an approval outranks anything somebody opened to read.
		{asking: true, palette: true, inspect: true, want: modeApprove},
		{palette: true, inspect: true, want: modePalette},
	} {
		a := newTestApp(t)
		a.asking.Set(tc.asking)
		a.paletteOpen.Set(tc.palette)
		a.inspectOpen.Set(tc.inspect)

		if got := a.mode(); got != tc.want {
			t.Errorf("mode(asking=%v palette=%v inspect=%v) = %d, want %d",
				tc.asking, tc.palette, tc.inspect, got, tc.want)
		}
	}
}

// The ring the hint line promises has to be the ring Tab walks, and it changes with the log pane and
// with whether the open conversation has a composer at all.
func TestNextRegionFollowsTheRing(t *testing.T) {
	for name, tc := range map[string]struct {
		state hintState
		from  region
		want  region
	}{
		"logs closed skips them":     {hintState{}, regTranscript, regComposer},
		"logs open are a station":    {hintState{LogOpen: true}, regTranscript, regLogs},
		"logs hand back to composer": {hintState{LogOpen: true}, regLogs, regComposer},
		"the composer wraps to list": {hintState{}, regComposer, regList},
		// An agent run renders no composer, so the ring is two stations and Tab goes back to the top.
		"read-only has no composer": {hintState{ReadOnly: true}, regTranscript, regList},
		"nowhere lands on the list": {hintState{}, regNone, regList},
	} {
		if got := nextRegion(tc.from, tc.state); got != tc.want {
			t.Errorf("%s: nextRegion(%s) = %s, want %s",
				name, regionName(tc.from), regionName(got), regionName(tc.want))
		}
	}
}

// The regression this whole file exists to prevent. The hint line is a PROMISE about the keys, and
// the two used to be maintained side by side: a fixed string in the template naming six keys, and a
// KeyMap assembled from conditional appends. They drifted the moment either changed.
//
// Only the Ctrl keys are checked, because they are the ones the root owns. The rest — j, k, Enter,
// the arrows — belong to whichever pane holds the keyboard and are focus-gated there.
func TestHintsOnlyNameKeysTheModeOffers(t *testing.T) {
	for _, m := range []mode{modeChat, modeInspect, modePalette, modeApprove} {
		for _, r := range []region{regNone, regList, regTranscript, regLogs, regComposer} {
			for _, s := range []hintState{{}, {Running: true}, {LogOpen: true}, {ReadOnly: true}} {
				a := newTestApp(t)
				setMode(t, a, m)
				km := a.KeyMap()

				for _, named := range ctrlKeysIn(hintsFor(m, r, s)) {
					if !offersCtrl(km, named) {
						t.Errorf("mode %d region %s promises Ctrl+%s, and its KeyMap does not offer it",
							m, regionName(r), strings.ToUpper(string(named)))
					}
				}
			}
		}
	}
}

// setMode puts the app into the mode whose key table is wanted. modeApprove needs a real pending ask
// because the option keys are minted from it.
func setMode(t *testing.T, a *app, m mode) {
	t.Helper()
	switch m {
	case modeApprove:
		ask, res := pendingAsk(t, a)
		t.Cleanup(func() { ask.Deny(); <-res })
	case modePalette:
		a.paletteOpen.Set(true)
	case modeInspect:
		a.inspectOpen.Set(true)
	}
}

// ctrlKeysIn pulls the letters out of every "Ctrl+X" the line names.
func ctrlKeysIn(hints string) []rune {
	var out []rune
	for _, part := range strings.Split(hints, " · ") {
		if after, ok := strings.CutPrefix(part, "Ctrl+"); ok && after != "" {
			out = append(out, unicodeLower([]rune(after)[0]))
		}
	}
	return out
}

func unicodeLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r - 'A' + 'a'
	}
	return r
}

// offersCtrl reports whether the table binds Ctrl+r. tui.KeyCtrlC is itself Rune('c').Ctrl(), so
// there is one shape to match and not two.
func offersCtrl(km gotui.KeyMap, r rune) bool {
	for _, b := range km {
		if b.Pattern.Rune == r && b.Pattern.Mod == gotui.ModCtrl {
			return true
		}
	}
	return false
}

// Every overlay has to be closable by Escape, and the binding has to PREEMPT. This is the bug that
// reached a user: a trapping modal ends its own KeyMap with OnPreemptStop(AnyKey), a catch-all that
// matches everything, and the preempt pass is the only pass that runs before it. An ordinary OnStop
// binding on the root is therefore never reached, and the workspace view could only be left by
// quitting the program.
func TestEveryOverlayIsEscapable(t *testing.T) {
	for _, m := range []mode{modeInspect, modePalette, modeApprove} {
		a := newTestApp(t)
		setMode(t, a, m)

		var found bool
		for _, b := range a.KeyMap() {
			if b.Pattern.Key != gotui.KeyEscape {
				continue
			}
			found = true
			if !b.Preempt || !b.Stop {
				t.Errorf("mode %d binds Escape with preempt=%v stop=%v; the modal's catch-all runs "+
					"after the preempt pass and would eat it", m, b.Preempt, b.Stop)
			}
		}
		if !found {
			t.Errorf("mode %d offers no Escape, so its overlay cannot be left", m)
		}
	}
}

// Quitting and cancelling are the two keys that hold everywhere, including under a trapping modal.
func TestEveryModeCanBeLeft(t *testing.T) {
	for _, m := range []mode{modeChat, modeInspect, modePalette, modeApprove} {
		a := newTestApp(t)
		setMode(t, a, m)
		km := a.KeyMap()

		for _, want := range []rune{'q', 'c'} {
			if !offersCtrl(km, want) {
				t.Errorf("mode %d does not offer Ctrl+%s", m, string(want))
			}
		}
	}
}

// In the chat mode the composer is a real text field, so the root may claim no bare letter: it would
// either be typed into the field or stolen from it, depending on which the dispatch table reached
// first.
func TestChatModeClaimsNoBareLetters(t *testing.T) {
	a := newTestApp(t)

	for _, b := range a.KeyMap() {
		if b.Pattern.Rune != 0 && b.Pattern.Mod == 0 {
			t.Errorf("the chat mode binds the bare rune %q; it would fight the composer for it", string(b.Pattern.Rune))
		}
		if b.Pattern.AnyRune {
			t.Error("the chat mode binds AnyRune; the composer owns typing")
		}
	}
}

// The palette is the one mode that DOES claim runes, and it may: the modal has trapped focus into an
// overlay with nothing focusable in it, so the composer's own focus-gated bindings cannot fire.
func TestPaletteModeClaimsTyping(t *testing.T) {
	a := newTestApp(t)
	a.paletteOpen.Set(true)

	var typing bool
	for _, b := range a.KeyMap() {
		if b.Pattern.AnyRune {
			typing = true
			if !b.Preempt || !b.Stop {
				t.Error("the palette's typing binding does not preempt; the modal's catch-all would eat it")
			}
		}
	}
	if !typing {
		t.Error("the palette offers no way to type into its filter")
	}
}

// A flash is something that HAPPENED, so it has to stop being true. The one-second tick that already
// redraws a running turn is what collects it.
func TestFlashExpiresOnItsOwn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := newTestApp(t)
		a.say("denied net")

		if got := a.hintLine(); got != "denied net" {
			t.Fatalf("hintLine = %q, want the message that was just raised", got)
		}

		time.Sleep(flashLife + time.Second)
		a.expireFlash()

		if got := a.hintLine(); strings.Contains(got, "denied") {
			t.Errorf("hintLine = %q, want the keys back once the message is stale", got)
		}
	})
}

// The context bar is derived, never written, so nothing can leave it describing a moment that has
// passed. These are the three states it has to tell apart.
func TestContextBarSaysWhereYouAre(t *testing.T) {
	a := newTestApp(t)

	if got := a.where(); got != "opening…" {
		t.Errorf("where() = %q before the workspace is open, want it to say so", got)
	}
	if got := a.activity(); got != "idle" {
		t.Errorf("activity() = %q with nothing running, want idle", got)
	}

	v := a.view.Get()
	v.Running = true
	a.view.Set(v)
	if got := a.activity(); !strings.HasPrefix(got, "⏵") {
		t.Errorf("activity() = %q with a turn in flight, want it marked as running", got)
	}
}

// A pane that holds the keyboard says so in its own title, which survives a terminal with no colour.
func TestPaneTitleMarksTheFocusedPane(t *testing.T) {
	if got := paneTitle(" logs ", false); got != " logs " {
		t.Errorf("paneTitle(unfocused) = %q, want it untouched", got)
	}
	if got := paneTitle(" logs ", true); !strings.HasPrefix(got, "▸") {
		t.Errorf("paneTitle(focused) = %q, want a marker on it", got)
	}
}
