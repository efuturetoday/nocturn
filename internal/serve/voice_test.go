package serve

import (
	"errors"
	"log/slog"
	"math"
	"testing"
	"time"
)

// The board runs one clock at 16 kHz while the model emits 24 kHz, and the conversion happens here
// rather than on the device: the ratio is exactly 3:2 and this side has CPU to spare, where a
// board's two cores are already carrying an audio front end.
func TestDownsample_ThreeSamplesBecomeTwo(t *testing.T) {
	in := pcm(make([]int16, 300))
	if got, want := len(downsample24to16(in))/2, 200; got != want {
		t.Errorf("300 samples became %d, want %d", got, want)
	}
}

// A tone must come back as the same tone, not as noise: a conversion that got the phase or the
// ordering wrong still produces the right number of bytes.
func TestDownsample_PreservesTheSignal(t *testing.T) {
	// 1 kHz at 24 kHz — 24 samples per cycle, well inside what 16 kHz can carry.
	const n = 2400
	src := make([]int16, n)
	for i := range src {
		src[i] = int16(12000 * math.Sin(2*math.Pi*1000*float64(i)/24000))
	}
	out := unpcm(downsample24to16(pcm(src)))

	// Compare against the same tone generated directly at 16 kHz. Interpolation error is bounded, so
	// this is a real check rather than a restatement of the implementation.
	var worst float64
	for i, got := range out {
		want := 12000 * math.Sin(2*math.Pi*1000*float64(i)/16000)
		if d := math.Abs(float64(got) - want); d > worst {
			worst = d
		}
	}
	if worst > 1500 { // ~12% of amplitude; linear interpolation at this ratio does far better
		t.Errorf("worst deviation %.0f from a 1 kHz reference — the tone did not survive", worst)
	}
}

// Silence in, silence out. A conversion that leaked a byte offset would turn this into a buzz.
func TestDownsample_SilenceStaysSilent(t *testing.T) {
	for _, v := range unpcm(downsample24to16(pcm(make([]int16, 240)))) {
		if v != 0 {
			t.Fatalf("silence produced %d", v)
		}
	}
}

// Frames arrive at whatever size the provider chose, including very short ones at a turn boundary.
func TestDownsample_TinyFrameIsNotCorrupted(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3} {
		out := downsample24to16(pcm(make([]int16, n)))
		if len(out)%2 != 0 {
			t.Errorf("%d samples produced %d bytes — not whole samples", n, len(out))
		}
	}
}

// A device holds two connections while a dropped one times out, and speech belongs to the newer: the
// older is the socket the device has already stopped reading.
func TestHubSendAudio_GoesToTheNewestConnection(t *testing.T) {
	h, old := hubWith(t, "hallway")
	fresh := audioConn("hallway")
	h.add(fresh)

	if !h.sendAudio("hallway", make([]byte, downFrameBytes), nil) {
		t.Fatal("the frame was refused by a device that has a live connection")
	}
	if len(old.audio) != 0 {
		t.Errorf("%d frames went to the connection the device had already left", len(old.audio))
	}
	if got := len(fresh.audio); got != 1 {
		t.Errorf("the newest connection holds %d frames, want 1", got)
	}
}

// The stall this addressing exists to prevent. A connection whose device is gone without a FIN has a
// writer blocked in a socket write that will not return, so its queue fills and never drains. Sending
// to every connection would WAIT on that queue, and nothing would end the wait: the session is live so
// abort stays silent, and the connection is not closed either. The socket that works would go quiet.
func TestHubSendAudio_ADeadConnectionDoesNotStallTheLiveOne(t *testing.T) {
	h, dead := hubWith(t, "hallway")
	for len(dead.audio) < cap(dead.audio) {
		dead.audio <- nil
	}
	fresh := audioConn("hallway")
	h.add(fresh)

	taken := make(chan bool, 1)
	go func() { taken <- h.sendAudio("hallway", make([]byte, downFrameBytes), nil) }()
	select {
	case ok := <-taken:
		if !ok {
			t.Error("the frame was refused")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sendAudio blocked on a connection the device had already left")
	}
}

// A barge-in must empty the queue the speech actually went to. What it leaves behind arrives after the
// device has flushed — it would obediently discard its own buffer and then be handed the same speech
// again.
func TestHubDropAudio_EmptiesTheConnectionSpeechWentTo(t *testing.T) {
	h, old := hubWith(t, "hallway")
	fresh := audioConn("hallway")
	h.add(fresh)

	old.audio <- make([]byte, downFrameBytes) // stale: sendAudio never fed this one
	fresh.audio <- make([]byte, downFrameBytes)

	if got, want := h.dropAudio("hallway"), downFrameBytes; got != want {
		t.Errorf("dropped %d bytes, want %d", got, want)
	}
	if len(fresh.audio) != 0 {
		t.Error("the connection the device is reading still holds speech it was told to drop")
	}
}

// A minute of speech nobody has taken is a device that is gone. Continuing would bill a live model
// into a backlog, and dropping from the middle of a reply only makes what survives incoherent.
func TestDeviceSink_AFullBacklogEndsTheSession(t *testing.T) {
	h, _ := hubWith(t, "hallway")
	s := newTestSink(h, "hallway")
	s.backlog = make([]byte, maxBacklog)

	if err := s.Play(speech(downFrameBytes)); !errors.Is(err, errDeviceGone) {
		t.Errorf("play on a full backlog = %v, want %v", err, errDeviceGone)
	}
}

// hubWith returns a hub holding one connection for device, with no socket behind it: sendAudio and
// dropAudio touch nothing but the queue.
func hubWith(t *testing.T, device string) (*hub, *conn) {
	t.Helper()
	h := newHub(defaultHeartbeat)
	c := audioConn(device)
	h.add(c)
	return h, c
}

// audioConn is a connection with a queue and no socket. Its capacity is the production one, so a test
// that fills it fills what a real link would.
func audioConn(device string) *conn {
	return &conn{
		device: device,
		audio:  make(chan []byte, 4),
		log:    slog.New(slog.DiscardHandler),
	}
}

func newTestSink(h *hub, device string) *deviceSink {
	return &deviceSink{hub: h, device: device, log: slog.New(slog.DiscardHandler)}
}

// speech is n bytes of 16 kHz output, expressed as the 24 kHz input that produces it.
func speech(n int) []byte { return pcm(make([]int16, n/2*3/2)) }

func pcm(samples []int16) []byte {
	b := make([]byte, len(samples)*2)
	for i, s := range samples {
		b[2*i] = byte(uint16(s))
		b[2*i+1] = byte(uint16(s) >> 8)
	}
	return b
}

func unpcm(b []byte) []int16 {
	s := make([]int16, len(b)/2)
	for i := range s {
		s[i] = int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8)
	}
	return s
}
