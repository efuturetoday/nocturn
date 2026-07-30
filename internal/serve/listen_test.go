package serve

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/speaker"
)

// A nil listener is the shape every daemon without a model runs in, and every caller relies on it
// behaving rather than being checked for.
func TestNilListenerIsUsable(t *testing.T) {
	var l *listener
	l.add(make([]byte, 640))
	if got := l.who(); got.Known() {
		t.Errorf("a nil listener reported %q", got.Name)
	}
}

func TestNewListenerNeedsBothHalves(t *testing.T) {
	profiles, err := speaker.OpenProfiles(t.TempDir() + "/voices.json")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)

	// No model.
	if got := newListener(nil, profiles, log); got != nil {
		t.Error("a listener was built without an embedder")
	}
	// No profiles.
	if got := newListener(&speaker.Embedder{}, nil, log); got != nil {
		t.Error("a listener was built without profiles")
	}
	// Profiles, but nobody enrolled: there is nothing to recognise anyone against, so recognising is
	// not something this daemon can do — and saying so up front beats embedding every window to
	// compare it with an empty set.
	if got := newListener(&speaker.Embedder{}, profiles, log); got != nil {
		t.Error("a listener was built with no voices enrolled")
	}
}

// Silence must not enter the window. The half-duplex gate sends zeros upstream while the board
// speaks, and a window of those describes nobody.
func TestListenerSkipsSilence(t *testing.T) {
	l := &listener{log: slog.New(slog.DiscardHandler)}

	l.add(make([]byte, 640))
	l.add(quiet(640))
	if len(l.speech) != 0 {
		t.Errorf("silence contributed %d samples", len(l.speech))
	}

	l.add(loud(640))
	if len(l.speech) != 320 {
		t.Errorf("speech contributed %d samples, want 320", len(l.speech))
	}
}

// The window is recent speech, not all of it: an embedding should describe how somebody sounds now.
func TestListenerWindowIsBounded(t *testing.T) {
	l := &listener{log: slog.New(slog.DiscardHandler), busy: true} // busy: never spawn an embedding

	frame := loud(640)
	for range 3 * listenWindow / 320 {
		l.add(frame)
	}
	if len(l.speech) > listenWindow {
		t.Errorf("window holds %d samples, want at most %d", len(l.speech), listenWindow)
	}
}

// The throttle is the point of the pacing: identification runs on a schedule rather than back to
// back, and a frame that arrives too soon is counted, not lost.
func TestListenerDefersWithinTheInterval(t *testing.T) {
	// busy stays false so this exercises the interval and not the older one-at-a-time guard; last is
	// set so the very first crossing of the minimum is already inside the window.
	l := &listener{log: slog.New(slog.DiscardHandler), last: time.Now()}

	l.add(loud(2 * listenMinimum)) // one frame carrying a full minimum window
	if len(l.speech) < listenMinimum {
		t.Fatalf("window holds %d samples, want at least %d", len(l.speech), listenMinimum)
	}
	if l.deferred != 1 {
		t.Errorf("deferred = %d, want 1 — the interval had not elapsed", l.deferred)
	}
	if l.busy {
		t.Error("an identification started inside the interval")
	}

	// Below the minimum nothing is deferred either, because nothing was ever going to run.
	short := &listener{log: slog.New(slog.DiscardHandler), last: time.Now()}
	short.add(loud(640))
	if short.deferred != 0 {
		t.Errorf("deferred = %d on a window too short to identify, want 0", short.deferred)
	}
}

// Short while the answer is still moving, long once it has settled: the difference between finding
// out who is in the room and checking they have not been replaced.
func TestListenerIntervalStretchesOnceStable(t *testing.T) {
	l := &listener{}
	for stable := range listenStable {
		l.stable = stable
		if got := l.interval(); got != listenInterval {
			t.Errorf("interval at %d agreements = %v, want %v", stable, got, listenInterval)
		}
	}
	l.stable = listenStable
	if got := l.interval(); got != listenSettled {
		t.Errorf("interval at %d agreements = %v, want %v", listenStable, got, listenSettled)
	}
}

// The log line exists to make a near-miss visible, so its rendering is worth pinning.
func TestRankingAndRunnerUp(t *testing.T) {
	none := []speaker.Match{}
	if got := ranking(none); got != "nobody enrolled" {
		t.Errorf("ranking(empty) = %q", got)
	}
	if got := runnerUp(none); got != "none" {
		t.Errorf("runnerUp(empty) = %q", got)
	}

	one := []speaker.Match{{Name: "oliver", Confidence: 0.71}}
	if got := ranking(one); got != "oliver=0.710" {
		t.Errorf("ranking(one) = %q, want oliver=0.710", got)
	}
	if got := runnerUp(one); got != "none" {
		t.Errorf("runnerUp(one) = %q, want none — there is nobody behind", got)
	}

	two := []speaker.Match{{Name: "oliver", Confidence: 0.71}, {Name: "anna", Confidence: 0.68}}
	if got := ranking(two); got != "oliver=0.710 anna=0.680" {
		t.Errorf("ranking(two) = %q", got)
	}
	// The gap is the number worth reading: three hundredths is two people about to be confused.
	if got := runnerUp(two); got != "anna=0.680 (behind by 0.030)" {
		t.Errorf("runnerUp(two) = %q", got)
	}

	many := make([]speaker.Match, 6)
	for i := range many {
		many[i] = speaker.Match{Name: "p", Confidence: 0.5}
	}
	if got := ranking(many); !strings.HasSuffix(got, "(+2 more)") {
		t.Errorf("ranking(six) = %q, want it to stop after four and say how many remain", got)
	}
}
