package voice_test

import (
	"log/slog"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/voice"
)

// managed builds a Manager over a driver whose live sessions the test scripts.
func managed(t *testing.T, sess *fakeSession) *voice.Manager {
	t.Helper()
	d := voice.New(&fakeLive{sess: sess}, toolset(t), allow(), gate.NewMemGrants(), nil)
	m := voice.NewManager(d, slog.New(slog.DiscardHandler))
	t.Cleanup(m.CloseAll)
	return m
}

// sink is the writing half a caller supplies: what a connection would do with speech and the one
// control signal. The Manager owns the reading half, so a test feeds the microphone through Feed.
type sink struct {
	played     chan []byte
	interrupts chan struct{}
}

func newSink() *sink {
	return &sink{played: make(chan []byte, 16), interrupts: make(chan struct{}, 4)}
}

func (s *sink) Play(pcm []byte) error { s.played <- pcm; return nil }
func (s *sink) Interrupt() error      { s.interrupts <- struct{}{}; return nil }
func (s *sink) Waiting(bool) error    { return nil }

// ending returns a callback plus the channel it reports through.
func ending() (voice.Ended, chan []agentkit.Message) {
	done := make(chan []agentkit.Message, 4)
	return func(_ string, transcript []agentkit.Message, _ error) { done <- transcript }, done
}

func TestManager_RunsUntilStopped(t *testing.T) {
	sess, dev := newSession(), newSink()
	m := managed(t, sess)
	ended, done := ending()

	m.Start("hallway", dev, nil, nil, ended)

	// Audio proves the session is really running, not merely registered.
	sess.push(agentkit.LiveAudio{PCM: []byte{0x01}})
	<-dev.played
	if !m.Active("hallway") {
		t.Fatal("session not reported active while it is playing")
	}

	m.Stop("hallway")
	if m.Active("hallway") {
		t.Error("still active after Stop")
	}
	<-done
}

// Stop must not return before the session has unwound: the driver cancels a pending tool call as it
// tears down, and returning early would leave an approval on somebody's phone able to answer a call
// that no longer exists.
func TestManager_StopWaitsForTheSessionToUnwind(t *testing.T) {
	sess, dev := newSession(), newSink()
	m := managed(t, sess)
	ended, done := ending()

	m.Start("hallway", dev, nil, nil, ended)
	sess.push(agentkit.LiveAudio{PCM: []byte{0x01}})
	<-dev.played

	m.Stop("hallway")
	select {
	case <-done:
	default:
		t.Fatal("Stop returned before the session finished")
	}
}

// A wake word during a conversation means the person is addressing the assistant again, not that
// they want two conversations at once.
func TestManager_StartReplacesARunningSession(t *testing.T) {
	first := newSession()
	dev := newSink()
	d := voice.New(&fakeLive{sess: first}, toolset(t), allow(), gate.NewMemGrants(), nil)
	m := voice.NewManager(d, slog.New(slog.DiscardHandler))
	t.Cleanup(m.CloseAll)

	ended, done := ending()
	m.Start("hallway", dev, nil, nil, ended)
	first.push(agentkit.LiveAudio{PCM: []byte{0x01}})
	<-dev.played

	// The second Start must have ended the first before it returns.
	m.Start("hallway", dev, nil, nil, ended)
	select {
	case <-done:
	default:
		t.Fatal("the replaced session was still running")
	}
	if !m.Active("hallway") {
		t.Error("no session active after replacing")
	}
}

// Two devices are two conversations. A speaker in the hallway and one in the kitchen must not share
// a session, or one person's question is answered into the other's room.
func TestManager_DevicesAreIndependent(t *testing.T) {
	hall, kitchen := newSession(), newSession()
	hallSink, kitchenSink := newSink(), newSink()

	// One driver per session here, because the fake hands out a fixed session.
	mHall := managed(t, hall)
	mKitchen := managed(t, kitchen)
	ended, _ := ending()

	mHall.Start("hallway", hallSink, nil, nil, ended)
	mKitchen.Start("kitchen", kitchenSink, nil, nil, ended)

	hall.push(agentkit.LiveAudio{PCM: []byte{0xAA}})
	if got := <-hallSink.played; got[0] != 0xAA {
		t.Errorf("hallway played %v", got)
	}
	select {
	case pcm := <-kitchenSink.played:
		t.Fatalf("kitchen played the hallway's audio: %v", pcm)
	default:
	}

	mHall.Stop("hallway")
	if !mKitchen.Active("kitchen") {
		t.Error("stopping one device ended the other's session")
	}
}

func TestManager_StopUnknownDeviceIsHarmless(t *testing.T) {
	m := managed(t, newSession())
	m.Stop("never-started") // must not panic or block
}

// After shutdown a new session would hold the daemon open on a conversation nobody can hear.
func TestManager_ClosedManagerStartsNothing(t *testing.T) {
	sess, dev := newSession(), newSink()
	m := managed(t, sess)
	m.CloseAll()

	m.Start("hallway", dev, nil, nil, nil)
	if m.Active("hallway") {
		t.Error("a closed manager started a session")
	}
}

func TestManager_CloseAllEndsEverything(t *testing.T) {
	sess, dev := newSession(), newSink()
	m := managed(t, sess)
	ended, done := ending()

	m.Start("hallway", dev, nil, nil, ended)
	sess.push(agentkit.LiveAudio{PCM: []byte{0x01}})
	<-dev.played

	m.CloseAll()
	select {
	case <-done:
	default:
		t.Fatal("CloseAll returned with a session still running")
	}
	if m.Active("hallway") {
		t.Error("still active after CloseAll")
	}
}

// The transcript is handed back rather than persisted here: where a spoken conversation belongs is
// the consumer's decision.
func TestManager_ReportsTheTranscript(t *testing.T) {
	sess, dev := newSession(), newSink()
	m := managed(t, sess)
	ended, done := ending()

	m.Start("hallway", dev, nil, nil, ended)
	sess.push(agentkit.LiveUserText{Text: "what time is it"})
	sess.push(agentkit.LiveTurnDone{})
	// Wait for the turn to be committed before stopping, so the transcript is not raced.
	sess.push(agentkit.LiveAudio{PCM: []byte{0x01}})
	<-dev.played

	m.Stop("hallway")
	transcript := <-done
	if len(transcript) != 1 || transcript[0].Content != "what time is it" {
		t.Errorf("transcript = %+v", transcript)
	}
}

// The session owns the microphone, not the caller: a standing connection outlives many
// conversations, so "the next chunk" means nothing without knowing which one is asking.
func TestManager_FeedReachesTheRunningSession(t *testing.T) {
	sess, out := newSession(), newSink()
	m := managed(t, sess)
	m.Start("hallway", out, nil, nil, nil)

	m.Feed("hallway", []byte{0x11, 0x22})
	if got := <-sess.audio; got[0] != 0x11 || got[1] != 0x22 {
		t.Errorf("uplink = %v", got)
	}
	m.Stop("hallway")
}

// Audio arriving with no conversation open is streamed into nothing — not an error, and not
// something that may block the connection's read loop.
func TestManager_FeedWithoutASessionIsDropped(t *testing.T) {
	m := managed(t, newSession())
	m.Feed("hallway", []byte{0x11}) // must not panic or block
	if m.Active("hallway") {
		t.Error("feeding started a session")
	}
}

// Ending a session must not look like the device disappearing: the connection underneath stands, and
// the next wake word has to work.
func TestManager_StoppingLeavesTheSinkUsable(t *testing.T) {
	first, out := newSession(), newSink()
	m := managed(t, first)

	m.Start("hallway", out, nil, nil, nil)
	first.push(agentkit.LiveAudio{PCM: []byte{0x01}})
	<-out.played
	m.Stop("hallway")

	// Same sink, second conversation.
	second := newSession()
	d := voice.New(&fakeLive{sess: second}, toolset(t), allow(), gate.NewMemGrants(), nil)
	m2 := voice.NewManager(d, slog.New(slog.DiscardHandler))
	t.Cleanup(m2.CloseAll)

	m2.Start("hallway", out, nil, nil, nil)
	second.push(agentkit.LiveAudio{PCM: []byte{0x02}})
	if got := <-out.played; got[0] != 0x02 {
		t.Errorf("second session played %v", got)
	}
}
