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

// ending returns a callback plus the channel it reports through.
func ending() (voice.Ended, chan []agentkit.Message) {
	done := make(chan []agentkit.Message, 4)
	return func(_ string, transcript []agentkit.Message, _ error) { done <- transcript }, done
}

func TestManager_RunsUntilStopped(t *testing.T) {
	sess, dev := newSession(), newDevice()
	m := managed(t, sess)
	ended, done := ending()

	m.Start("hallway", dev, nil, ended)

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
	sess, dev := newSession(), newDevice()
	m := managed(t, sess)
	ended, done := ending()

	m.Start("hallway", dev, nil, ended)
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
	dev := newDevice()
	d := voice.New(&fakeLive{sess: first}, toolset(t), allow(), gate.NewMemGrants(), nil)
	m := voice.NewManager(d, slog.New(slog.DiscardHandler))
	t.Cleanup(m.CloseAll)

	ended, done := ending()
	m.Start("hallway", dev, nil, ended)
	first.push(agentkit.LiveAudio{PCM: []byte{0x01}})
	<-dev.played

	// The second Start must have ended the first before it returns.
	m.Start("hallway", dev, nil, ended)
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
	hallDev, kitchenDev := newDevice(), newDevice()

	// One driver per session here, because the fake hands out a fixed session.
	mHall := managed(t, hall)
	mKitchen := managed(t, kitchen)
	ended, _ := ending()

	mHall.Start("hallway", hallDev, nil, ended)
	mKitchen.Start("kitchen", kitchenDev, nil, ended)

	hall.push(agentkit.LiveAudio{PCM: []byte{0xAA}})
	if got := <-hallDev.played; got[0] != 0xAA {
		t.Errorf("hallway played %v", got)
	}
	select {
	case pcm := <-kitchenDev.played:
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
	sess, dev := newSession(), newDevice()
	m := managed(t, sess)
	m.CloseAll()

	m.Start("hallway", dev, nil, nil)
	if m.Active("hallway") {
		t.Error("a closed manager started a session")
	}
}

func TestManager_CloseAllEndsEverything(t *testing.T) {
	sess, dev := newSession(), newDevice()
	m := managed(t, sess)
	ended, done := ending()

	m.Start("hallway", dev, nil, ended)
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
	sess, dev := newSession(), newDevice()
	m := managed(t, sess)
	ended, done := ending()

	m.Start("hallway", dev, nil, ended)
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
