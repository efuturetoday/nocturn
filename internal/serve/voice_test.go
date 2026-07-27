package serve

import (
	"math"
	"testing"
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
