package serve

import (
	"log/slog"
	"testing"

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
